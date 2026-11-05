package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/store"
)

// auditEntry collects what a handler adds to its admin_audit row: whom it
// acted on and a few details (counts, flags; never an email).
type auditEntry struct {
	target  *uuid.UUID
	details map[string]any
}

type auditKey struct{}

func auditOf(r *http.Request) *auditEntry {
	e, _ := r.Context().Value(auditKey{}).(*auditEntry)
	return e
}

// auditTarget names the user an admin call acts on.
func auditTarget(r *http.Request, id uuid.UUID) {
	if e := auditOf(r); e != nil {
		e.target = &id
	}
}

// auditDetail adds a detail to the call's audit row.
func auditDetail(r *http.Request, key string, value any) {
	if e := auditOf(r); e != nil {
		e.details[key] = value
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// audited writes an admin_audit row for every authorized admin call once it
// has been answered: the route pattern, the status, the request id, the actor
// (the key's fingerprint, or the operator's user id for a token), and what the
// handler added (D88). A failed write is logged; the answer has gone out by then.
func (a admin) audited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := &auditEntry{details: map[string]any{}}
		r = r.WithContext(context.WithValue(r.Context(), auditKey{}, e))
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		action, requestID := r.Method+" "+r.URL.Path, ""
		if ri := httpx.Info(r.Context()); ri != nil {
			requestID = ri.RequestID
			if ri.Route != "" {
				action = ri.Route
			}
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		entry := store.AuditEntry{Action: action, TargetUser: e.target, Status: sw.status, RequestID: requestID, Details: e.details}
		if act, ok := adminActorFrom(r.Context()); ok && act.isKey() {
			entry.KeyFingerprint = act.fingerprint
		} else if ok {
			entry.ActorUser = &act.user
		}
		if err := a.h.st.Audit(ctx, entry); err != nil {
			a.h.log.ErrorContext(ctx, "admin audit write failed", "err", err, "action", action)
		}
	})
}

// resolve reads {id} (a full user id or its first 8 hex digits) and answers
// 404 or 409 itself when it does not name exactly one user.
func (a admin) resolve(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, bool) {
	id, err := a.h.st.ResolveUser(r.Context(), r.PathValue(param))
	switch {
	case errors.Is(err, store.ErrNotFound):
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, http.StatusNotFound, param+" not found")
		return uuid.Nil, false
	case errors.Is(err, store.ErrAmbiguous):
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, http.StatusConflict, "the short id matches more than one user; use the full id")
		return uuid.Nil, false
	case err != nil:
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return uuid.Nil, false
	}
	return id, true
}

// grantAccess is POST /v1/admin/users/{id}/access: beta access without a code.
func (a admin) grantAccess(w http.ResponseWriter, r *http.Request) { a.setAccess(w, r, true) }

// revokeAccess is DELETE /v1/admin/users/{id}/access.
func (a admin) revokeAccess(w http.ResponseWriter, r *http.Request) { a.setAccess(w, r, false) }

func (a admin) setAccess(w http.ResponseWriter, r *http.Request, grant bool) {
	id, ok := a.resolve(w, r, "id")
	if !ok {
		return
	}
	auditTarget(r, id)
	auditDetail(r, "grant", grant)
	if err := a.h.st.SetBetaAccess(r.Context(), id, grant); err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.info(r, "beta access set by the operator", "grant", grant, "target_user", id.String()[:8])
	w.WriteHeader(http.StatusNoContent)
}

// resyncHint is POST /v1/admin/users/{id}/resync-hint: GET /v1/me/sync then
// tells the app to run a full re-sync (resync_requested_at), for an account
// missing data the phone still has.
func (a admin) resyncHint(w http.ResponseWriter, r *http.Request) {
	id, ok := a.resolve(w, r, "id")
	if !ok {
		return
	}
	auditTarget(r, id)
	if err := a.h.st.RequestResync(r.Context(), id); err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.info(r, "full re-sync requested", "target_user", id.String()[:8])
	w.WriteHeader(http.StatusNoContent)
}

// audit is GET /v1/admin/audit?since= (RFC 3339 or YYYY-MM-DD; default 7 days).
func (a admin) audit(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r.URL.Query().Get("since"), time.Now())
	if err != nil {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, err.Error())
		return
	}
	list, err := a.h.st.AuditSince(r.Context(), since, 1000)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	httpx.WriteJSON(w, 200, list)
}
