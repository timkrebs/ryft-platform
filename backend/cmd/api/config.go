package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// remoteConfig is GET /v1/config: every feature flag evaluated for the caller.
func (h *handler) remoteConfig(w http.ResponseWriter, r *http.Request) {
	all, err := h.st.Flags(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := flags.Evaluate(all, userOf(r).ID)
	for k, on := range out {
		metrics.FlagEvaluations.WithLabelValues(k, onOff(on)).Inc()
	}
	httpx.WriteJSON(w, 200, model.RemoteConfig{Flags: out, EvaluatedAt: time.Now().UTC().Truncate(time.Second), TTLSec: flags.TTL,
		PrivacyPolicyVersion: h.beta.PrivacyPolicyVersion, PrivacyPolicyURL: h.beta.PrivacyPolicyURL})
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// maxAllowList bounds a flag's allow-list (testers, not a user segment).
const maxAllowList = 1000

// admin serves the operator's /v1/admin routes (feature flags, invite codes).
// They are not user routes: the bearer is ADMIN_API_KEY, no user is looked up.
// keyID (the key's fingerprint) goes into the log of every change.
type admin struct {
	h     *handler
	keyID string
}

// keyFingerprint names an admin key in logs without revealing it: the first 8
// hex digits of its SHA-256.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

func (a admin) flags(w http.ResponseWriter, r *http.Request) {
	all, err := a.h.st.Flags(r.Context())
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	httpx.WriteJSON(w, 200, all)
}

func (a admin) putFlag(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var in model.FlagInput
	if err := httpx.ReadJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteDecodeError(w, err)
		return
	}
	msg := ""
	switch {
	case !flags.ValidKey(key):
		msg = "key must be dotted lowercase segments, e.g. train.enabled (at most 64 characters)"
	case in.Enabled == nil || in.RolloutPct == nil:
		msg = "enabled and rollout_pct are required"
	case *in.RolloutPct < 0 || *in.RolloutPct > 100:
		msg = "rollout_pct must be 0-100"
	case len(in.AllowUserIDs) > maxAllowList:
		msg = "allow_user_ids holds at most 1000 ids"
	}
	if msg != "" {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 422, msg)
		return
	}
	f, created, err := a.h.st.PutFlag(r.Context(), model.FeatureFlag{Key: key, Enabled: *in.Enabled,
		RolloutPct: *in.RolloutPct, AllowUserIDs: in.AllowUserIDs})
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.h.log.InfoContext(r.Context(), "feature flag set", "key", f.Key, "enabled", f.Enabled,
		"rollout_pct", f.RolloutPct, "allow_users", len(f.AllowUserIDs), "created", created, "admin_key", a.keyID)
	status := 200
	if created {
		status = 201
	}
	httpx.WriteJSON(w, status, f)
}

func (a admin) deleteFlag(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	err := a.h.st.DeleteFlag(r.Context(), key)
	if errors.Is(err, store.ErrNotFound) {
		metrics.AdminRequests.WithLabelValues("invalid").Inc()
		httpx.WriteError(w, 404, "no such flag")
		return
	}
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.h.log.InfoContext(r.Context(), "feature flag deleted", "key", key, "admin_key", a.keyID)
	w.WriteHeader(http.StatusNoContent)
}

// adminOnly lets a request through only with "Authorization: Bearer
// <ADMIN_API_KEY>", compared in constant time.
func adminOnly(key string, next http.Handler) http.Handler {
	want := []byte("Bearer " + key)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			metrics.AdminRequests.WithLabelValues("unauthorized").Inc()
			w.Header().Set("WWW-Authenticate", `Bearer realm="ryft-admin"`)
			httpx.WriteError(w, http.StatusUnauthorized, "admin key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
