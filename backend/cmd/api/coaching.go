package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/wearables"
)

func (h *handler) coachingNow() time.Time {
	if h.coachingClock != nil {
		return h.coachingClock()
	}
	return time.Now()
}

func (h *handler) coachingRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /v1/users/goal-profile", h.nutritionGate(h.n2rConsent(func(w http.ResponseWriter, r *http.Request) {
		out, err := h.st.CoachingGoal(r.Context(), userOf(r).ID)
		nutritionAnswer(w, r, 200, out, err)
	})))
	api.HandleFunc("PUT /v1/users/goal-profile", h.nutritionGate(h.n2rConsent(nutritionBody(200,
		func(ctx context.Context, user uuid.UUID, profile model.GoalProfile) (model.GoalProfile, error) {
			return h.st.PutCoachingGoal(ctx, user, profile, h.coachingNow())
		}))))
	api.HandleFunc("DELETE /v1/users/goal-profile", func(w http.ResponseWriter, r *http.Request) {
		nutritionAnswer(w, r, 204, nil, h.st.DeleteCoachingGoal(r.Context(), userOf(r).ID))
	})
	api.HandleFunc("PUT /v1/coaching/feedback/{date}", h.nutritionGate(h.n2rConsent(func(w http.ResponseWriter, r *http.Request) {
		var in model.CoachingFeedback
		if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
			httpx.WriteDecodeError(w, err)
			return
		}
		out, err := h.st.PutCoachingFeedback(r.Context(), userOf(r).ID, r.PathValue("date"), in, h.coachingNow())
		nutritionAnswer(w, r, 200, out, err)
	})))
	api.HandleFunc("GET /v1/coaching/daily-briefing", h.nutritionGate(h.n2rConsent(func(w http.ResponseWriter, r *http.Request) {
		out, err := h.st.CoachingBriefing(r.Context(), userOf(r).ID, h.coachingNow())
		nutritionAnswer(w, r, 200, out, err)
	})))
}

func (a admin) evaluationMetrics(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = time.Now().UTC().AddDate(0, 0, -30).Format(time.DateOnly)
	}
	if date, err := time.Parse(time.DateOnly, since); err != nil || date.Year() < 2000 || date.Year() > 2200 {
		httpx.WriteError(w, 422, "since must be a calendar date")
		return
	}
	out, err := a.h.st.CoachingCohorts(r.Context(), since)
	if out == nil && err == nil {
		out = []model.EvaluationCohort{}
	}
	nutritionAnswer(w, r, 200, out, err)
}

type partnerConfig struct {
	secrets   map[string]string
	fetch     wearables.Fetchers
	token     string
	transport http.RoundTripper
}

func loadPartnerConfig() (partnerConfig, error) {
	out := partnerConfig{secrets: map[string]string{"garmin": os.Getenv("COACHING_GARMIN_WEBHOOK_SECRET"),
		"suunto": os.Getenv("COACHING_SUUNTO_WEBHOOK_SECRET")}, token: os.Getenv("COACHING_PARTNER_FETCH_TOKEN")}
	garminURL, suuntoURL := os.Getenv("COACHING_GARMIN_FETCH_URL"), os.Getenv("COACHING_SUUNTO_FETCH_URL")
	if httpx.CloudRunService() || os.Getenv("CLOUD_RUN_JOB") != "" {
		if out.secrets["garmin"] != "" || out.secrets["suunto"] != "" || out.token != "" || garminURL != "" || suuntoURL != "" {
			return out, errors.New("coaching relays require an approved connector before Cloud Run activation")
		}
	}
	if out.secrets["garmin"] != "" && out.secrets["garmin"] == out.secrets["suunto"] {
		return out, errors.New("coaching relays must use distinct signing keys")
	}
	var err error
	out.fetch, err = wearables.NewFetchers(garminURL, suuntoURL)
	if err != nil {
		return out, err
	}
	for kind, key := range out.secrets {
		if key != "" && len(key) < 32 {
			return out, errors.New("partner webhook key must be at least 32 bytes")
		}
		if key != "" && kind == "suunto" && (out.fetch[kind] == nil || out.token == "") {
			return out, errors.New("suunto webhook requires its authenticated fetch bridge")
		}
	}
	return out, nil
}

func (h *handler) partnerWebhook(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := h.partners.secrets[kind]
		if secret == "" {
			httpx.NotFound(w, r)
			return
		}
		signatures := r.Header.Values("X-HMAC-SHA256-Signature")
		if len(signatures) != 1 || signatures[0] == "" {
			httpx.WriteError(w, 401, "invalid wearable signature")
			return
		}
		const deadline = 1700 * time.Millisecond
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(deadline))
		ctx, cancel := context.WithTimeout(r.Context(), deadline)
		defer cancel()
		payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wearables.PartnerBodyLimit))
		if err != nil {
			httpx.WriteError(w, 413, "wearable payload exceeds the request budget")
			return
		}
		if !wearables.Signed(payload, signatures[0], secret) {
			httpx.WriteError(w, 401, "invalid wearable signature")
			return
		}
		contentTypes := r.Header.Values("Content-Type")
		if len(contentTypes) != 1 {
			httpx.WriteError(w, 415, "unsupported wearable content type")
			return
		}
		mediaType, _, err := mime.ParseMediaType(contentTypes[0])
		if err != nil || !(mediaType == "application/json" || kind == "suunto" && mediaType == "application/x-www-form-urlencoded") {
			httpx.WriteError(w, 415, "unsupported wearable content type")
			return
		}
		if kind == "suunto" && json.Valid(payload) != (mediaType == "application/json") {
			httpx.WriteError(w, 422, "invalid wearable notification")
			return
		}
		deliveries, err := wearables.DecodeDelivery(kind, payload)
		if err != nil || len(deliveries) > 128 {
			httpx.WriteError(w, 422, "invalid wearable notification")
			return
		}
		if len(deliveries) > 0 {
			if err := h.st.EnqueueWearable(ctx, kind, h.beta.PrivacyPolicyVersion, deliveries); err != nil {
				httpx.WriteError(w, 503, "wearable queue unavailable")
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (h *handler) processWearables(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Retention applies to existing data even after disabling the last provider.
	if err := h.st.PurgeWearableInbox(ctx, h.coachingNow()); err != nil {
		return errors.New("wearable queue retention failed")
	}
	var providers []string
	for provider, secret := range h.partners.secrets {
		if secret != "" {
			providers = append(providers, provider)
		}
	}
	if len(providers) == 0 {
		return nil
	}
	all, err := h.st.Flags(ctx)
	if err != nil {
		return errors.New("read wearable rollout")
	}
	for range 6 {
		job, err := h.st.ClaimWearable(ctx, h.coachingNow(), h.beta.PrivacyPolicyVersion, providers)
		if err != nil {
			return errors.New("wearable claim failed")
		}
		if job == nil {
			return nil
		}
		if !flags.Enabled(all, model.FlagNutrition, job.User) {
			if err := h.st.FinishWearable(ctx, *job, h.coachingNow(), true); err != nil {
				return errors.New("wearable queue completion failed")
			}
			continue
		}
		body, err := h.partners.fetch.Fetch(ctx, job.Source, job.Delivery, h.partners.token, h.partners.transport)
		if err == nil {
			err = h.st.ApplyWearable(ctx, *job, body, h.beta.PrivacyPolicyVersion, h.coachingNow())
		}
		if err != nil {
			if finalize := h.st.FinishWearable(ctx, *job, h.coachingNow(), false); finalize != nil {
				return errors.New("wearable queue completion failed")
			}
			return errors.New("wearable processing failed, retry scheduled")
		}
	}
	return nil
}

func (h *handler) refreshCoaching(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	all, err := h.st.Flags(ctx)
	if err != nil {
		return errors.New("read coaching rollout")
	}
	for _, flag := range all {
		if flag.Key == model.FlagNutrition && flag.Enabled && (flag.RolloutPct > 0 || len(flag.AllowUserIDs) > 0) {
			err = h.st.RefreshCoaching(ctx, h.coachingNow(), h.beta.PrivacyPolicyVersion, func(user uuid.UUID) bool {
				return flags.Enabled(all, model.FlagNutrition, user)
			})
			if err != nil {
				return errors.New("coaching evaluation pass failed")
			}
			break
		}
	}
	return nil
}
