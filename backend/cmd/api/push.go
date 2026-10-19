package main

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/notify"
)

// apnsToken is a device token as the app sends it (hex; 32 bytes today, Apple
// may make them longer).
var apnsToken = regexp.MustCompile(`^[0-9a-f]{64,200}$`)

// registerPush is POST /v1/devices/{id}/push: the install's APNs token.
func (h *handler) registerPush(w http.ResponseWriter, r *http.Request) {
	device, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, 400, "device id must be a UUID")
		return
	}
	var reg model.PushRegistration
	if err := httpx.ReadJSON(w, r, &reg, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	reg.Token = strings.ToLower(strings.TrimSpace(reg.Token))
	switch {
	case !apnsToken.MatchString(reg.Token):
		httpx.WriteError(w, 422, "token must be the APNs device token in hex")
		return
	case reg.Env != "sandbox" && reg.Env != "production":
		httpx.WriteError(w, 422, "env must be sandbox or production")
		return
	}
	reg.Locale = notify.Language(reg.Locale)
	if err := h.st.RegisterPushToken(r.Context(), userOf(r).ID, device, reg); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deletePush is DELETE /v1/devices/{id}/push (sign-out); idempotent.
func (h *handler) deletePush(w http.ResponseWriter, r *http.Request) {
	device, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, 400, "device id must be a UUID")
		return
	}
	if err := h.st.DeletePushToken(r.Context(), userOf(r).ID, device); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// patchNotifications is PATCH /v1/me/notifications: categories on or off,
// quiet hours set (local "HH:MM") or cleared (null).
func (h *handler) patchNotifications(w http.ResponseWriter, r *http.Request) {
	var p model.NotificationPatch
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	for c := range p.Categories {
		if !slices.Contains(model.NotifyCategories, c) {
			httpx.WriteError(w, 422, "unknown category "+c+" (MORNING_SCORE, SESSION_SCORED, PR, SYNC_STALE)")
			return
		}
	}
	if q := p.QuietHours.Value; q != nil {
		_, _, okFrom := notify.ParseClock(q.From)
		_, _, okTo := notify.ParseClock(q.To)
		if !okFrom || !okTo {
			httpx.WriteError(w, 422, "quiet_hours.from and .to must be local times HH:MM")
			return
		}
	}
	u, err := h.st.UpdateNotificationPrefs(r.Context(), userOf(r).ID, p)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, u.Notifications)
}
