package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// MergeAnswer is POST /v1/admin/users/{id}/merge-into/{to}: the plan (dry run)
// or what happened.
type MergeAnswer struct {
	From       mergeSide          `json:"from"`
	To         mergeSide          `json:"to"`
	DryRun     bool               `json:"dry_run"`
	Tables     []store.MergeTable `json:"tables"`
	Consent    string             `json:"consent"`     // copied, kept (to has one), none
	BetaAccess string             `json:"beta_access"` // copied, kept, none
	Rescore    *dayRange          `json:"rescore"`     // to's days rescored after the move
	Deleted    *AccountDeleted    `json:"deleted"`     // from, through the account deletion path
}

type mergeSide struct {
	ID         uuid.UUID `json:"id"`
	ShortID    string    `json:"short_id"`
	DaysScored int       `json:"days_scored"`
}

type dayRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// merge is POST /v1/admin/users/{id}/merge-into/{to}[?dry_run=1][&force=1]
// (D90): everything of {id} moves to {to}, in one transaction; to's rows win
// where both have one; to is rescored over the moved data's days; {id} is then
// deleted like DELETE /v1/me (Supabase auth user and Apple grant included). A
// second call answers 404: from is gone. It refuses to merge the account with
// more scored days into the smaller one unless force=1.
func (a admin) merge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dryRun, force := q.Get("dry_run") == "1", q.Get("force") == "1"
	from, ok := a.resolve(w, r, "id")
	if !ok {
		return
	}
	to, ok := a.resolve(w, r, "to")
	if !ok {
		return
	}
	auditTarget(r, from)
	auditDetail(r, "into", to.String())
	auditDetail(r, "dry_run", dryRun)
	auditDetail(r, "force", force)
	if from == to {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, "an account cannot be merged into itself")
		return
	}
	ctx := r.Context()
	ans := MergeAnswer{DryRun: dryRun, From: mergeSide{ID: from, ShortID: from.String()[:8]}, To: mergeSide{ID: to, ShortID: to.String()[:8]}}
	var err error
	if ans.From.DaysScored, err = a.h.st.ScoredDays(ctx, from); err == nil {
		ans.To.DaysScored, err = a.h.st.ScoredDays(ctx, to)
	}
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	if ans.From.DaysScored > ans.To.DaysScored && !force {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, http.StatusConflict, fmt.Sprintf("%s has %d scored days and %s %d: that looks like the wrong way round "+
			"(merge the smaller into the larger), or add force=1", ans.From.ShortID, ans.From.DaysScored, ans.To.ShortID, ans.To.DaysScored))
		return
	}
	// The move, the rescore and the deletion run to the end even if the client hangs up.
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), deleteTimeout)
	defer cancel()
	res, err := a.h.st.MergeUsers(work, from, to, dryRun)
	switch {
	case errors.Is(err, store.ErrFromNotFound):
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, http.StatusNotFound, "from not found")
		return
	case errors.Is(err, store.ErrToNotFound):
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, http.StatusNotFound, "to not found")
		return
	case err != nil:
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	ans.Tables, ans.Consent, ans.BetaAccess = res.Tables, res.Consent, res.BetaAccess
	ans.Rescore, err = a.rescoreRange(work, to, res.FirstRecord)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	var moved int64
	for _, t := range res.Tables {
		moved += t.Moved
	}
	auditDetail(r, "moved", moved)
	if dryRun {
		metrics.AdminRequests.WithLabelValues("ok").Inc()
		httpx.WriteJSON(w, 200, ans)
		return
	}
	if err := a.h.recordsFor(work, to, res.ExerciseIDs); err != nil {
		a.h.log.ErrorContext(work, "merge: personal records", "err", err)
	}
	a.h.cache.forget(to)
	if ans.Rescore != nil {
		ev := model.IngestedEvent{UserID: to, Days: []string{ans.Rescore.From, ans.Rescore.To}, Reason: "merge",
			RequestID: requestID(r)}
		if err := a.h.bus.PublishIngested(work, ev, "merge:"+from.String()+":"+to.String()); err != nil {
			// The data is moved; a POST /v1/recompute or rescore_all.sh catches up.
			a.h.log.ErrorContext(work, "merge: publish rescore", "err", err)
		}
	}
	gone, err := a.h.eraseAccount(work, from)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, fmt.Errorf("merge: the data is moved, deleting %s failed (run the merge again): %w", ans.From.ShortID, err))
		return
	}
	ans.Deleted = &gone
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.h.log.InfoContext(work, "accounts merged", "from", ans.From.ShortID, "into", ans.To.ShortID, "moved", moved,
		"admin_key", a.keyID)
	httpx.WriteJSON(w, 200, ans)
}

// rescoreRange is to's local days from the first moved record to today, at
// most scoring.MaxRangeDays; nil when nothing was moved.
func (a admin) rescoreRange(ctx context.Context, to uuid.UUID, first *time.Time) (*dayRange, error) {
	if first == nil {
		return nil, nil
	}
	_, loc, err := a.h.st.GetUser(ctx, to)
	if err != nil {
		return nil, err
	}
	today := time.Now().In(loc)
	start := first.In(loc)
	if oldest := today.AddDate(0, 0, -(scoring.MaxRangeDays - 1)); start.Before(oldest) {
		start = oldest
	}
	return &dayRange{From: start.Format(dayLayout), To: today.Format(dayLayout)}, nil
}
