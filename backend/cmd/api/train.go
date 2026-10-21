package main

// Train (M3b): the exercise library, plans with an enrolment, logged sessions
// (docs/API.md → Train). Rules live in internal/train (validation, merge) and
// internal/scoring (load, records, progression); this file is HTTP.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// failTrain answers the errors the Train store and rules return.
func failTrain(w http.ResponseWriter, r *http.Request, err error, what string) {
	var p train.Problem
	switch {
	case errors.As(err, &p):
		httpx.WriteError(w, http.StatusUnprocessableEntity, p.Msg)
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, what+" not found")
	case errors.Is(err, store.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "built-in "+what+"s cannot be changed")
	case errors.Is(err, store.ErrTaken):
		httpx.WriteError(w, http.StatusConflict, "this id belongs to another "+what)
	default:
		httpx.Fail(w, r, err)
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "id must be a UUID")
		return id, false
	}
	return id, true
}

func german(r *http.Request) bool { return train.German(r.Header.Get("Accept-Language")) }

func localizePlan(p *model.Plan, de bool) {
	for d := range p.Days {
		localizeDay(&p.Days[d], de)
	}
}

func localizeDay(d *model.PlanDay, de bool) {
	for e := range d.Exercises {
		if x := d.Exercises[e].Exercise; x != nil {
			train.Localize(x, de)
		}
	}
}

// ---- exercises ----------------------------------------------------------------------

func (h *handler) exercises(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ExerciseFilter{Q: strings.TrimSpace(q.Get("q")), Muscle: q.Get("muscle"), Kind: q.Get("kind"),
		IncludeArchived: q.Get("include_archived") == "true"}
	if f.Muscle != "" && !slices.Contains(model.Muscles, f.Muscle) {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "unknown muscle")
		return
	}
	if f.Kind != "" && !model.KnownExerciseKinds[f.Kind] {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "unknown kind")
		return
	}
	list, err := h.st.Exercises(r.Context(), userOf(r).ID, f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	de := german(r)
	for k := range list {
		train.Localize(&list[k], de)
	}
	slices.SortStableFunc(list, func(a, b model.Exercise) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	if list == nil {
		list = []model.Exercise{}
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (h *handler) createExercise(w http.ResponseWriter, r *http.Request) {
	var in model.ExerciseInput
	if err := httpx.ReadJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	ex, err := train.NewExercise(in)
	if err != nil {
		failTrain(w, r, err, "exercise")
		return
	}
	ex.LoadFactor = scoring.DefaultLoadFactor(ex.Kind)
	out, created, err := h.st.SaveCustomExercise(r.Context(), userOf(r).ID, ex)
	if err != nil {
		failTrain(w, r, err, "exercise")
		return
	}
	train.Localize(&out, german(r))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, out)
}

func (h *handler) patchExercise(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p model.ExercisePatch
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	h.updateExercise(w, r, id, p, http.StatusOK)
}

// archiveExercise is DELETE /v1/exercises/{id}: sessions and plans keep it.
func (h *handler) archiveExercise(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	archived := true
	h.updateExercise(w, r, id, model.ExercisePatch{Archived: &archived}, http.StatusNoContent)
}

func (h *handler) updateExercise(w http.ResponseWriter, r *http.Request, id uuid.UUID, p model.ExercisePatch, status int) {
	out, err := h.st.UpdateCustomExercise(r.Context(), userOf(r).ID, id, func(e model.Exercise) (model.Exercise, error) {
		next, err := train.ApplyPatch(e, p)
		if next.Kind != e.Kind {
			next.LoadFactor = scoring.DefaultLoadFactor(next.Kind)
		}
		return next, err
	})
	if err != nil {
		failTrain(w, r, err, "exercise")
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	train.Localize(&out, german(r))
	httpx.WriteJSON(w, status, out)
}

func (h *handler) exerciseHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusUnprocessableEntity, "limit must be 1–200")
			return
		}
		limit = n
	}
	ctx, user := r.Context(), userOf(r).ID
	ex, err := h.st.Exercise(ctx, user, id)
	if err != nil {
		failTrain(w, r, err, "exercise")
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sessions, err := h.st.ExerciseSessions(ctx, user, id, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	prs, err := h.st.ExercisePRs(ctx, user, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := model.ExerciseHistory{ExerciseID: id, PRs: prs, Sessions: []model.ExerciseHistorySession{}, E1RM: []model.TrendPoint{}}
	best := map[string]float64{}
	var days []string
	for _, s := range sessions {
		day := s.StartedAt.In(loc).Format(dayLayout)
		out.Sessions = append(out.Sessions, model.ExerciseHistorySession{SessionID: s.SessionID, Date: day, StartedAt: s.StartedAt, Sets: s.Sets})
		bw := scoring.DefaultBodyweightKG
		if s.Bodyweight != nil {
			bw = *s.Bodyweight
		}
		for _, st := range s.Sets {
			if st.CompletedAt == nil || st.IsWarmup || st.Reps == nil || *st.Reps <= 0 {
				continue
			}
			if ex.LoadType == model.LoadBodyweight && (st.WeightKG == nil || *st.WeightKG <= 0) {
				continue // e1RM only for loaded bodyweight sets
			}
			wgt, ok := scoring.EffectiveWeight(ex, st, bw)
			if !ok || wgt <= 0 {
				continue
			}
			v := scoring.E1RM(wgt, *st.Reps)
			if _, seen := best[day]; !seen {
				days = append(days, day)
			}
			best[day] = max(best[day], v)
		}
	}
	slices.Sort(days)
	for _, d := range days {
		out.E1RM = append(out.E1RM, model.TrendPoint{Day: d, Value: math.Round(best[d]*10) / 10})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ---- plans ------------------------------------------------------------------------------

func (h *handler) plans(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), userOf(r).ID
	builtin, mine, err := h.st.PlanSummaries(ctx, user)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	active, err := h.st.ActiveEnrollment(ctx, user)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, model.PlansOverview{Builtin: builtin, Mine: mine, Active: active})
}

func (h *handler) plan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := h.st.Plan(r.Context(), userOf(r).ID, id)
	if err != nil {
		failTrain(w, r, err, "plan")
		return
	}
	localizePlan(&p, german(r))
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *handler) putPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var p model.Plan
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyTrain); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if p.ID == uuid.Nil {
		p.ID = id
	}
	if p.ID != id {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "id must match the path")
		return
	}
	if err := train.CheckPlan(p); err != nil {
		failTrain(w, r, err, "plan")
		return
	}
	ctx, user := r.Context(), userOf(r).ID
	created, err := h.st.SavePlan(ctx, user, p)
	if err != nil {
		failTrain(w, r, err, "plan")
		return
	}
	out, err := h.st.Plan(ctx, user, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	localizePlan(&out, german(r))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, out)
}

func (h *handler) deletePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.st.DeletePlan(r.Context(), userOf(r).ID, id); err != nil {
		failTrain(w, r, err, "plan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) enroll(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	e, created, err := h.st.Enroll(r.Context(), userOf(r).ID, id)
	if err != nil {
		failTrain(w, r, err, "plan")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, e)
}

func (h *handler) unenroll(w http.ResponseWriter, r *http.Request) {
	if err := h.st.PauseEnrollment(r.Context(), userOf(r).ID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// planToday is the active enrolment's current day with the weights to use.
func (h *handler) planToday(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), userOf(r).ID
	e, err := h.st.ActiveEnrollment(ctx, user)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if e == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p, err := h.st.Plan(ctx, user, e.PlanID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	day, err := store.CurrentPlanDay(p, *e)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent) // the plan has no day at this position
		return
	}
	localizeDay(&day, german(r))
	now := time.Now()
	bw, err := h.st.LastBodyweight(ctx, user, now)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fallback := scoring.DefaultBodyweightKG
	if bw != nil {
		fallback = *bw
	}
	out := model.PlanToday{Enrollment: *e, Day: day, Targets: []model.PlanTarget{}}
	for _, pe := range day.Exercises {
		attempts, err := h.st.PlanExerciseAttempts(ctx, user, pe, day.ID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		e1rm := 0.0
		if pe.TargetKind == model.TargetPct1RM && pe.Exercise != nil {
			sets, err := h.st.LoggedSets(ctx, user, []uuid.UUID{pe.ExerciseID}, now, uuid.Nil)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			e1rm = scoring.BestE1RMs(sets, map[uuid.UUID]model.Exercise{pe.ExerciseID: *pe.Exercise}, fallback)[pe.ExerciseID]
		}
		last, err := h.st.LastWorkingSets(ctx, user, pe.ExerciseID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out.Targets = append(out.Targets, model.PlanTarget{PlanExerciseID: pe.ID, ExerciseID: pe.ExerciseID, Sets: pe.Sets,
			RepsMin: pe.RepsMin, RepsMax: pe.RepsMax, TargetKind: pe.TargetKind,
			TargetKG: scoring.NextTargetKG(pe, p.Progression, attempts, e1rm), RestSec: pe.RestSec, LastTime: last})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ---- sessions ------------------------------------------------------------------------------

func (h *handler) sessions(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		writeRangeError(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !model.KnownSessionStatuses[status] {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "status must be in_progress, completed or discarded")
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a, _ := time.ParseInLocation(dayLayout, from, loc)
	b, _ := time.ParseInLocation(dayLayout, to, loc)
	list, err := h.st.SessionSummaries(r.Context(), userOf(r).ID, a, b.AddDate(0, 0, 1), status, false)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if list == nil {
		list = []model.SessionSummary{}
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s, err := h.st.Session(r.Context(), userOf(r).ID, id)
	if err != nil {
		failTrain(w, r, err, "session")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

// putSession saves the app's whole session document (after every set).
func (h *handler) putSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body model.Session
	if err := httpx.ReadJSON(w, r, &body, httpx.MaxBodyTrain); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if body.ID == uuid.Nil {
		body.ID = id
	}
	if body.ID != id {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "id must match the path")
		return
	}
	if err := train.CheckSession(&body, time.Now().UTC()); err != nil {
		failTrain(w, r, err, "session")
		return
	}
	ctx, user := r.Context(), userOf(r).ID
	res, err := h.st.SaveSession(ctx, user, body)
	if err != nil {
		failTrain(w, r, err, "session")
		return
	}
	if res.Conflict {
		httpx.WriteJSON(w, http.StatusConflict, res.Session) // the server's copy: merge and PUT again
		return
	}
	metrics.TrainSetsLogged.Add(float64(res.NewSets))
	if res.Before != nil && res.Before.Status == model.SessionCompleted {
		// An edit (or discard) of a completed session: rescore it and its days.
		out, err := h.settle(ctx, r, res.Session, res.Before, false)
		if err != nil {
			h.failSettle(w, r, err)
			return
		}
		res.Session = out
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, res.Session)
}

// completeSession finishes a session: its load, records and workout, the
// enrolment's next day, and the day rescored.
func (h *handler) completeSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body model.SessionComplete
	if err := httpx.ReadJSON(w, r, &body, httpx.MaxBodyDefault); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteDecodeError(w, err)
		return
	}
	ctx, user := r.Context(), userOf(r).ID
	s, err := h.st.Session(ctx, user, id)
	if err != nil {
		failTrain(w, r, err, "session")
		return
	}
	if s.Status == model.SessionDiscarded {
		httpx.WriteError(w, http.StatusConflict, "a discarded session cannot be completed")
		return
	}
	wasCompleted := s.Status == model.SessionCompleted
	end := scoring.SessionEnd(s)
	switch {
	case body.EndedAt != nil:
		end = *body.EndedAt
	case s.EndedAt == nil && end.Equal(s.StartedAt):
		end = time.Now().UTC()
	}
	if end.Before(s.StartedAt) {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "ended_at must not be before started_at")
		return
	}
	s.EndedAt = &end
	if body.BodyweightKG != nil {
		bw := min(max(*body.BodyweightKG, 30), 300)
		s.BodyweightKG = &bw
	}
	s.Status = model.SessionCompleted
	out, err := h.settle(ctx, r, s, nil, true)
	if err != nil {
		h.failSettle(w, r, err)
		return
	}
	if !wasCompleted {
		metrics.TrainSessionsCompleted.Inc()
		if s.PlanDayID != nil {
			if err := h.st.AdvanceEnrollment(ctx, user, *s.PlanDayID); err != nil {
				h.log.ErrorContext(ctx, "advance enrolment", "err", err)
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, user := r.Context(), userOf(r).ID
	gone, err := h.st.DeleteSession(ctx, user, id)
	if err != nil {
		failTrain(w, r, err, "session")
		return
	}
	if gone.Status == model.SessionCompleted {
		if err := h.recordsFor(ctx, user, exerciseIDs(gone)); err != nil {
			h.log.ErrorContext(ctx, "records after delete", "err", err)
		}
		// The deletion stands; if the queue is down, scoring's periodic pass
		// rescores today and yesterday.
		if err := h.rescoreDays(ctx, r, user, gone, nil); err != nil {
			h.log.ErrorContext(ctx, "rescore after delete", "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// errQueue: the session is stored, but its day could not be queued (503, retry).
var errQueue = errors.New("queue unavailable")

func (h *handler) failSettle(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errQueue) {
		w.Header().Set("Retry-After", "30")
		httpx.WriteError(w, http.StatusServiceUnavailable, "queue unavailable")
		return
	}
	failTrain(w, r, err, "session")
}

// settle brings a completed (or discarded) session's derived data up to date:
// its load and attached workout (completed only), the records of every
// exercise it or its previous version touched, and its days queued for
// scoring. complete publishes session.completed; an edit publishes
// ingested for the old and the new day. It returns the stored session.
func (h *handler) settle(ctx context.Context, r *http.Request, s model.Session, before *model.Session, complete bool) (model.Session, error) {
	user := userOf(r).ID
	prev, err := h.st.SessionPRs(ctx, user, s.ID)
	if err != nil {
		return s, err
	}
	if s.Status == model.SessionCompleted {
		load, err := h.engine.SessionLoadNow(ctx, user, s)
		if err != nil {
			return s, err
		}
		workoutID, err := h.previewWorkout(ctx, user, s)
		if err != nil {
			return s, err
		}
		// Completing: the session.completed event commits with the completion (D101).
		var also func(pgx.Tx) error
		if complete && h.outbox != nil {
			m, err := h.completedEvent(r, user, s)
			if err != nil {
				return s, err
			}
			also = func(tx pgx.Tx) error { return outbox.Insert(ctx, tx, "api", m) }
		}
		if err := h.st.CompleteSessionWith(ctx, user, s.ID, store.Completion{EndedAt: *s.EndedAt, BodyweightKG: s.BodyweightKG,
			MuscularTRIMP: load.TRIMP, Strain: load.Strain, VolumeKG: load.VolumeKG, WorkoutID: workoutID}, also); err != nil {
			if also != nil && !errors.Is(err, store.ErrTaken) && !errors.Is(err, store.ErrNotFound) {
				h.log.ErrorContext(ctx, "complete session with its event", "err", err)
				return s, errQueue
			}
			return s, err
		}
		if also != nil {
			h.outbox.Kick()
		}
	}
	ids := exerciseIDs(s)
	if before != nil {
		ids = append(ids, exerciseIDs(*before)...)
	}
	if err := h.recordsFor(ctx, user, ids); err != nil {
		return s, err
	}
	out, err := h.st.Session(ctx, user, s.ID)
	if err != nil {
		return s, err
	}
	countNewPRs(prev, out.PRs)
	h.cache.forget(user)
	if complete {
		if h.outbox != nil {
			return out, nil // its event committed with the completion
		}
		ev, id, err := h.completed(r, user, out)
		if err != nil {
			return out, err
		}
		if err := h.bus.PublishSessionCompleted(ctx, ev, id); err != nil {
			h.log.ErrorContext(ctx, "publish session completed", "err", err)
			return out, errQueue
		}
		return out, nil
	}
	return out, h.rescoreDays(ctx, r, user, out, before)
}

// completed is the session.completed event of s and its id (stable per completion:
// completing the same session at the same time again is the same trigger).
func (h *handler) completed(r *http.Request, user uuid.UUID, s model.Session) (model.SessionCompletedEvent, string, error) {
	loc, err := h.location(r)
	if err != nil {
		return model.SessionCompletedEvent{}, "", err
	}
	ev := model.SessionCompletedEvent{UserID: user, SessionID: s.ID, Day: s.StartedAt.In(loc).Format(dayLayout), RequestID: requestID(r)}
	return ev, fmt.Sprintf("session:%s:%s:%d", user, s.ID, s.EndedAt.UnixMilli()), nil
}

func (h *handler) completedEvent(r *http.Request, user uuid.UUID, s model.Session) (events.Message, error) {
	ev, id, err := h.completed(r, user, s)
	if err != nil {
		return events.Message{}, err
	}
	return events.SessionCompleted(ev, id)
}

// rescoreDays queues the local days of a session and its previous version.
func (h *handler) rescoreDays(ctx context.Context, r *http.Request, user uuid.UUID, s model.Session, before *model.Session) error {
	loc, err := h.location(r)
	if err != nil {
		return err
	}
	days := []string{s.StartedAt.In(loc).Format(dayLayout)}
	if before != nil {
		if d := before.StartedAt.In(loc).Format(dayLayout); d != days[0] {
			days = append(days, d)
		}
	}
	ev := model.IngestedEvent{UserID: user, Days: days, Reason: "session", RequestID: requestID(r)}
	if err := h.bus.PublishIngested(ctx, ev, ""); err != nil {
		h.log.ErrorContext(ctx, "publish session change", "err", err)
		return errQueue
	}
	return nil
}

// previewWorkout is the workout scoring will attach the session to (the same
// rule, D60): a matching HealthKit workout not held by another session, else
// the one it creates.
func (h *handler) previewWorkout(ctx context.Context, user uuid.UUID, s model.Session) (uuid.UUID, error) {
	ws, err := h.st.WorkoutsBetween(ctx, user, s.StartedAt.Add(-12*time.Hour), scoring.SessionEnd(s).Add(12*time.Hour))
	if err != nil {
		return uuid.Nil, err
	}
	taken := map[uuid.UUID]bool{}
	for _, w := range ws {
		if w.SessionID != nil && *w.SessionID != s.ID {
			taken[w.ID] = true
		}
	}
	if w := scoring.MatchWorkout(s, ws, taken); w != nil {
		return w.ID, nil
	}
	return train.SessionWorkoutID(user, s.ID), nil
}

// recordsFor recomputes the user's records of the given exercises from all
// their completed sets.
func (h *handler) recordsFor(ctx context.Context, user uuid.UUID, ids []uuid.UUID) error {
	exs, err := h.st.VisibleExercises(ctx, user, ids)
	if err != nil {
		return err
	}
	for id, ex := range exs {
		sets, err := h.st.LoggedSets(ctx, user, []uuid.UUID{id}, time.Now().Add(24*time.Hour), uuid.Nil)
		if err != nil {
			return err
		}
		if err := h.st.ReplacePRs(ctx, user, id, scoring.PersonalRecords(sets, ex)); err != nil {
			return err
		}
	}
	return nil
}

func exerciseIDs(s model.Session) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(s.Exercises))
	for _, e := range s.Exercises {
		ids = append(ids, e.ExerciseID)
	}
	return ids
}

// countNewPRs counts the records a session holds now but did not before.
func countNewPRs(before, after []model.PR) {
	had := map[string]bool{}
	for _, p := range before {
		had[p.ExerciseID.String()+p.Kind] = true
	}
	for _, p := range after {
		if !had[p.ExerciseID.String()+p.Kind] {
			metrics.TrainPRs.WithLabelValues(p.Kind).Inc()
		}
	}
}
