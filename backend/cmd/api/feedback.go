package main

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Feedback limits (docs/API.md → POST /v1/feedback).
const (
	maxFeedbackMessage = 4000 // characters
	maxFeedbackField   = 64
	maxRequestIDs      = 20
	maxDiagnostics     = 32 << 10
	adminFeedbackLimit = 500
)

var feedbackCategories = map[string]bool{model.FeedbackBug: true, model.FeedbackIdea: true,
	model.FeedbackData: true, model.FeedbackOther: true}

// postFeedback stores a report from the app. The message is never logged.
func (h *handler) postFeedback(w http.ResponseWriter, r *http.Request) {
	var f model.FeedbackPost
	if err := httpx.ReadJSON(w, r, &f, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	category, msg := feedbackError(&f)
	if msg != "" {
		httpx.WriteError(w, 422, msg)
		return
	}
	id, err := h.st.AddFeedback(r.Context(), userOf(r).ID, f, category)
	if e, ok := store.IsTooMuchFeedback(err); ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(e.RetryAfter.Seconds())))
		httpx.WriteError(w, http.StatusTooManyRequests, e.Error())
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	metrics.FeedbackReceived.WithLabelValues(category).Inc()
	h.log.InfoContext(r.Context(), "feedback received", "feedback_id", id, "category", category,
		"request_ids", len(f.RequestIDs))
	w.WriteHeader(http.StatusNoContent)
}

// feedbackError checks a report: its category (other when omitted) or the 422 message.
func feedbackError(f *model.FeedbackPost) (category, msg string) {
	category = model.FeedbackOther
	if f.Category != nil {
		category = *f.Category
	}
	switch {
	case strings.TrimSpace(f.Message) == "" || utf8.RuneCountInString(f.Message) > maxFeedbackMessage:
		return "", "message must have 1-4000 characters"
	case !feedbackCategories[category]:
		return "", "category must be bug, idea, data or other"
	case tooLong(f.AppVersion) || tooLong(f.Build) || tooLong(f.DeviceModel) || tooLong(f.OSVersion):
		return "", "app_version, build, device_model and os_version have at most 64 characters"
	case len(f.RequestIDs) > maxRequestIDs:
		return "", "at most 20 request_ids"
	case len(f.Diagnostics) > maxDiagnostics:
		return "", "diagnostics has at most 32 KiB"
	}
	for _, id := range f.RequestIDs {
		if id == "" || len(id) > maxFeedbackField {
			return "", "request_ids are 1-64 characters each"
		}
	}
	if d := bytes.TrimSpace(f.Diagnostics); len(d) > 0 && string(d) != "null" && d[0] != '{' {
		return "", "diagnostics must be a JSON object"
	}
	return category, ""
}

func tooLong(s *string) bool { return s != nil && utf8.RuneCountInString(*s) > maxFeedbackField }

// ---- admin: feedback and the data-quality view ------------------------------------

// feedback is GET /v1/admin/feedback?since=: reports since a time (RFC 3339 or
// YYYY-MM-DD, UTC; default the last 7 days), newest first.
func (a admin) feedback(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r.URL.Query().Get("since"), time.Now())
	if err != nil {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, err.Error())
		return
	}
	list, err := a.h.st.FeedbackSince(r.Context(), since, adminFeedbackLimit)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	auditDetail(r, "n", len(list))
	a.h.log.InfoContext(r.Context(), "admin feedback read", "since", since, "n", len(list), "admin_key", a.keyID)
	httpx.WriteJSON(w, 200, list)
}

func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return now.Add(-7 * 24 * time.Hour), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(dayLayout, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("since must be RFC 3339 or YYYY-MM-DD")
}

// resolveFeedback is POST /v1/admin/feedback/{id}/resolve.
func (a admin) resolveFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 400, "id must be a number")
		return
	}
	err = a.h.st.ResolveFeedback(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 404, "no such feedback")
		return
	}
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.h.log.InfoContext(r.Context(), "feedback resolved", "feedback_id", id, "admin_key", a.keyID)
	w.WriteHeader(http.StatusNoContent)
}

// users is GET /v1/admin/users[?emails=1]: the data-quality view. Every call
// is logged with the admin key's fingerprint, and whether emails were shown.
func (a admin) users(w http.ResponseWriter, r *http.Request) {
	emails := r.URL.Query().Get("emails") == "1"
	list, err := a.h.st.AdminUsers(r.Context(), emails)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	auditDetail(r, "emails", emails)
	auditDetail(r, "n", len(list))
	a.h.log.InfoContext(r.Context(), "admin users read", "emails", emails, "n", len(list), "admin_key", a.keyID)
	httpx.WriteJSON(w, 200, list)
}
