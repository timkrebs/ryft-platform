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
// has been answered: the route pattern, the status, the request id, the key's
// fingerprint, and what the handler added (D88). A failed write is logged; the
// answer has gone out by then.
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
		if err := a.h.st.Audit(ctx, store.AuditEntry{KeyFingerprint: a.keyID, Action: action, TargetUser: e.target,
			Status: sw.status, RequestID: requestID, Details: e.details}); err != nil {
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
	a.h.log.InfoContext(r.Context(), "beta access set by the operator", "grant", grant, "target_user", id.String()[:8],
		"admin_key", a.keyID)
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

// adminRoutes is /v1/admin/: the admin key first, then the audit around every
// authorized call, the timeout and the routes.
func adminRoutes(h *handler, key string, timeout time.Duration) http.Handler {
	a, adm := admin{h: h, keyID: keyFingerprint(key)}, http.NewServeMux()
	adm.HandleFunc("GET /v1/admin/flags", a.flags)
	adm.HandleFunc("PUT /v1/admin/flags/{key}", a.putFlag)
	adm.HandleFunc("DELETE /v1/admin/flags/{key}", a.deleteFlag)
	adm.HandleFunc("POST /v1/admin/invites", a.createInvite)
	adm.HandleFunc("GET /v1/admin/invites", a.invites)
	adm.HandleFunc("DELETE /v1/admin/invites/{code}", a.deleteInvite)
	adm.HandleFunc("GET /v1/admin/feedback", a.feedback)
	adm.HandleFunc("POST /v1/admin/feedback/{id}/resolve", a.resolveFeedback)
	adm.HandleFunc("GET /v1/admin/users", a.users)
	adm.HandleFunc("POST /v1/admin/users/{id}/access", a.grantAccess)
	adm.HandleFunc("DELETE /v1/admin/users/{id}/access", a.revokeAccess)
	adm.HandleFunc("GET /v1/admin/audit", a.audit)
	adm.HandleFunc("/v1/admin/", httpx.NotFound)
	return adminOnly(key, a.audited(httpx.Timeout(timeout, httpx.WithRoutes(adm))))
}
