package main

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// maxJournalEntries bounds one day's PUT (25 built-ins and 50 custom fit).
const maxJournalEntries = 100

// behaviorsFor is the user's catalog with names in the request's language.
func (h *handler) behaviorsFor(r *http.Request) ([]model.Behavior, error) {
	list, err := h.st.Behaviors(r.Context(), userOf(r).ID)
	if err != nil {
		return nil, err
	}
	de := german(r)
	for k := range list {
		if de {
			list[k].Name = list[k].NameDE
		}
	}
	return list, nil
}

func (h *handler) behaviors(w http.ResponseWriter, r *http.Request) {
	list, err := h.behaviorsFor(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, list)
}

func (h *handler) patchBehaviors(w http.ResponseWriter, r *http.Request) {
	var p model.BehaviorsPatch
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	err := h.st.SetBehaviorsEnabled(r.Context(), userOf(r).ID, p.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, 422, "every id must be a built-in or one of your own behaviors")
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h.behaviors(w, r)
}

func (h *handler) createBehavior(w http.ResponseWriter, r *http.Request) {
	var in model.BehaviorInput
	if err := httpx.ReadJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	b := model.Behavior{ID: in.ID, NameEN: strings.TrimSpace(in.Name), Kind: in.Kind, Category: "custom"}
	b.NameDE = b.NameEN
	if in.NameDE != nil {
		b.NameDE = strings.TrimSpace(*in.NameDE)
	}
	if in.Category != nil {
		b.Category = *in.Category
	}
	okName := func(s string) bool { n := utf8.RuneCountInString(s); return n >= 1 && n <= 60 }
	switch {
	case in.ID == uuid.Nil:
		httpx.WriteError(w, 422, "id is required (the app generates it)")
		return
	case !okName(b.NameEN) || !okName(b.NameDE):
		httpx.WriteError(w, 422, "name and name_de must have 1-60 characters")
		return
	case !slices.Contains(model.BehaviorKinds, b.Kind):
		httpx.WriteError(w, 422, "kind must be yes_no, count or time")
		return
	case !slices.Contains(model.BehaviorCategories, b.Category):
		httpx.WriteError(w, 422, "unknown category")
		return
	}
	out, created, err := h.st.SaveCustomBehavior(r.Context(), userOf(r).ID, b)
	switch {
	case errors.Is(err, store.ErrTaken):
		httpx.WriteError(w, 409, "id belongs to a built-in or another user's behavior")
		return
	case errors.Is(err, store.ErrLimit):
		httpx.WriteError(w, 422, "at most 50 custom behaviors")
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	}
	if german(r) {
		out.Name = out.NameDE
	}
	status := 200
	if created {
		status = 201
	}
	httpx.WriteJSON(w, status, out)
}

func (h *handler) deleteBehavior(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, 400, "id must be a UUID")
		return
	}
	err = h.st.ArchiveBehavior(r.Context(), userOf(r).ID, id)
	if errors.Is(err, store.ErrForbidden) {
		httpx.WriteError(w, 403, "built-in behaviors cannot be deleted; turn them off with PATCH /v1/behaviors")
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// journalDay reads {day} and answers the JournalDay as stored.
func (h *handler) journalDay(w http.ResponseWriter, r *http.Request) {
	day, now, ok := h.journalPath(w, r)
	if !ok {
		return
	}
	h.writeJournalDay(w, r, day, now)
}

func (h *handler) journalPath(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return "", time.Time{}, false
	}
	day := r.PathValue("day")
	if _, err := time.Parse(dayLayout, day); err != nil {
		httpx.WriteError(w, 400, "day must be YYYY-MM-DD")
		return "", time.Time{}, false
	}
	return day, time.Now().In(loc), true
}

func (h *handler) writeJournalDay(w http.ResponseWriter, r *http.Request, day string, now time.Time) {
	at, entries, err := h.st.JournalDay(r.Context(), userOf(r).ID, day)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if at != nil {
		u := at.UTC()
		at = &u
	}
	httpx.WriteJSON(w, 200, model.JournalDay{Day: day, Answered: at != nil, AnsweredAt: at,
		Editable: behavior.Editable(day, now), Entries: entries})
}

// putJournal replaces one day's journal with what the sheet showed.
func (h *handler) putJournal(w http.ResponseWriter, r *http.Request) {
	day, now, ok := h.journalPath(w, r)
	if !ok {
		return
	}
	if !behavior.Editable(day, now) {
		httpx.WriteError(w, 422, "a journal day can be saved for today and the 14 days before")
		return
	}
	var p model.JournalPut
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if len(p.Entries) > maxJournalEntries {
		httpx.WriteError(w, 422, "at most 100 entries")
		return
	}
	list, err := h.st.Behaviors(r.Context(), userOf(r).ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	kinds := make(map[uuid.UUID]string, len(list))
	for _, b := range list {
		kinds[b.ID] = b.Kind
	}
	seen := map[uuid.UUID]bool{}
	for _, e := range p.Entries {
		kind, ok := kinds[e.BehaviorID]
		if !ok {
			httpx.WriteError(w, 422, "behavior "+e.BehaviorID.String()+" is not a built-in or one of your own")
			return
		}
		if seen[e.BehaviorID] {
			httpx.WriteError(w, 422, "behavior "+e.BehaviorID.String()+" appears twice")
			return
		}
		seen[e.BehaviorID] = true
		if err := behavior.CheckEntry(kind, e); err != nil {
			httpx.WriteError(w, 422, e.BehaviorID.String()+": "+err.Error())
			return
		}
	}
	if err := h.st.PutJournalDay(r.Context(), userOf(r).ID, day, p.Entries, kinds); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	metrics.JournalSaves.Inc()
	h.cache.forget(userOf(r).ID) // the behavior-impact card may change
	h.writeJournalDay(w, r, day, now)
}

// impacts computes the behaviors' effects on the morning after for the 90
// entry days before today (local): on request, D76.
func (h *handler) impacts(r *http.Request, today time.Time, list []model.Behavior, caller string) ([]model.BehaviorImpact, error) {
	ctx, user := r.Context(), userOf(r).ID
	answers, err := h.st.JournalAnswers(ctx, user, today.AddDate(0, 0, -behavior.WindowDays).Format(dayLayout),
		today.AddDate(0, 0, -1).Format(dayLayout))
	if err != nil {
		return nil, err
	}
	scores, err := h.st.DailyScores(ctx, user, today.AddDate(0, 0, -(behavior.WindowDays-1)).Format(dayLayout), today.Format(dayLayout))
	if err != nil {
		return nil, err
	}
	mornings := make(map[string]behavior.Morning, len(scores))
	for _, s := range scores {
		mornings[s.Day] = behavior.Morning{Recovery: s.Recovery, HRV: s.HRVms, SleepPerf: s.SleepPerformance}
	}
	var chosen []model.Behavior
	for _, b := range list {
		if b.Enabled || len(answers[b.ID]) > 0 {
			chosen = append(chosen, b)
		}
	}
	start := time.Now()
	out := behavior.Impacts(user, chosen, answers, mornings)
	metrics.BehaviorImpactSeconds.WithLabelValues(caller).Observe(time.Since(start).Seconds())
	return out, nil
}

func (h *handler) behaviorImpact(w http.ResponseWriter, r *http.Request) {
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	list, err := h.behaviorsFor(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := h.impacts(r, today, list, "impact")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, model.BehaviorImpacts{WindowDays: behavior.WindowDays, ComputedAt: now.UTC().Truncate(time.Second), Impacts: out})
}
