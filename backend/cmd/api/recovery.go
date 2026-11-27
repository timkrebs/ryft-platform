package main

import (
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// Recovery without a watch (docs/API.md → Recovery without a watch; D291–D295, D298, D299): the morning check-in, today's
// recovery status and the session RPE of a workout.
//
// The gates: the two writes and the status answer 404 (no code) while the flag recovery.checkin is off for the
// account, and the two writes need consent (warn acts as on, as the Nutrition writes). The reads and both DELETEs
// need neither: the owner's access and erasure. No log line holds a rating, a time or a score: the routes log their
// pattern, not the path, and a store failure is logged without its error (a Postgres error can quote a value).

const (
	// checkinMinSleep and checkinMaxSleep bound wake_time − bedtime (1–16 h).
	checkinMinSleep = time.Hour
	checkinMaxSleep = 16 * time.Hour
	// checkinFutureSlack is how far ahead of the clock a wake_time may be (a skewed phone clock).
	checkinFutureSlack = 5 * time.Minute
	// checkinMaxRangeDays bounds one GET /v1/recovery/checkins call.
	checkinMaxRangeDays = 400
	maxTZOffsetSec      = 14 * 3600
)

func (h *handler) recoveryRoutes(api *http.ServeMux) {
	api.HandleFunc("PUT /v1/recovery/checkins/{date}", recoveryRoute(h.recoveryFlag(h.recoveryConsent(h.putCheckin))))
	api.HandleFunc("GET /v1/recovery/checkins/{date}", recoveryRoute(h.getCheckin))
	api.HandleFunc("DELETE /v1/recovery/checkins/{date}", recoveryRoute(h.deleteCheckin))
	api.HandleFunc("GET /v1/recovery/checkins", recoveryRoute(h.listCheckins))
	api.HandleFunc("GET /v1/recovery/status", recoveryRoute(h.recoveryFlag(h.recoveryStatus)))
	api.HandleFunc("PUT /v1/workouts/{id}/rpe", recoveryRoute(h.recoveryFlag(h.recoveryConsent(h.putWorkoutRPE))))
	api.HandleFunc("DELETE /v1/workouts/{id}/rpe", recoveryRoute(h.deleteWorkoutRPE))
}

// recoveryRoute logs the route's pattern as the path: the date and the workout id stay out of the access line.
func recoveryRoute(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if info := httpx.Info(r.Context()); info != nil && r.Pattern != "" {
			info.LogPath = r.Pattern
		}
		fn(w, r)
	}
}

// recoveryFlag answers 404 without a code while recovery.checkin is off for the caller (what an older server
// answers too: the app hides the feature and drops no data).
func (h *handler) recoveryFlag(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		on, err := h.st.RecoveryEnabled(r.Context(), userOf(r).ID)
		if err != nil {
			recoveryFail(w, r, err)
			return
		}
		if !on {
			httpx.NotFound(w, r)
			return
		}
		fn(w, r)
	}
}

// recoveryConsent is the consent gate of the two writes: they store health data. CONSENT_ENFORCE=warn acts as on.
func (h *handler) recoveryConsent(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gate.Consent(w, r, h.recoveryBeta(), userOf(r), "api") {
			fn(w, r)
		}
	}
}

// recoveryBeta is the gate configuration of the two writes: warn acts as on.
func (h *handler) recoveryBeta() config.Beta {
	b := h.beta
	if b.Consent == config.ModeWarn {
		b.Consent = config.ModeOn
	}
	return b
}

// recoveryGrant is the consent that admitted the caller, for the store to check again in its write transaction
// (D300); nil when consent is not enforced.
func (h *handler) recoveryGrant(r *http.Request) *model.Consent {
	if h.recoveryBeta().Consent != config.ModeOn {
		return nil
	}
	return userOf(r).Consent
}

// recoveryConsentChanged answers consent_required, as the gate does, when err says the grant that admitted the write
// was withdrawn or replaced before it committed (nothing was stored).
func (h *handler) recoveryConsentChanged(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, store.ErrHealthKitConsentChanged) {
		return false
	}
	gate.Consent(w, r, h.recoveryBeta(), model.User{}, "api")
	return true
}

// recoveryFail answers 500 without the store's error in the log: a Postgres error can quote a stored value.
func recoveryFail(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.Fail(w, r, errors.New("recovery store operation failed"))
}

// scoreCheckin is the store's CheckinScorer: the pure check-in score (scoring.CheckinScore, D292) and the
// confounders of the evening before (periodization.CheckinConfounders, D294). scoring imports the store, so the
// store takes it from here.
func scoreCheckin(f store.CheckinFacts) store.CheckinResult {
	in := scoring.CheckinInput{
		Bedtime: f.Report.Bedtime, WakeTime: f.Report.WakeTime, TZOffsetSec: f.Report.TZOffsetSec,
		Quality: f.Report.Quality, Freshness: f.Report.Freshness, Soreness: f.Report.Soreness,
		SleepBaseMin: f.SleepBaseMin, SleepBaseSource: f.SleepBaseSource, N2R: f.N2R,
	}
	for _, p := range f.Prior {
		in.Prior = append(in.Prior, scoring.CheckinRating{Quality: p[0], Freshness: p[1]})
	}
	for _, n := range f.PriorNights {
		in.PriorMidpoints = append(in.PriorMidpoints, scoring.SleepMidpointMin(n.Bedtime, n.Wake, n.TZOffsetSec))
	}
	r := scoring.CheckinScore(in)
	c := r.Components
	return store.CheckinResult{
		SleepDurationMin: r.SleepDurationMin,
		RecoveryScore:    r.RecoveryScore,
		Components: model.RecoveryComponents{Wellness: c.Wellness, WellnessAbsolute: c.WellnessAbsolute,
			WellnessPersonal: c.WellnessPersonal, Duration: c.Duration, Regularity: c.Regularity, N2R: c.N2R},
		AcuteDrop:    r.AcuteDrop,
		DropReason:   r.DropReason,
		Confounders:  periodization.CheckinConfounders(r.AcuteDrop, f.N2RTags, f.Journal, f.Report.Bedtime),
		ModelVersion: scoring.CheckinModelVersion,
	}
}

// checkinDate reads {date} (YYYY-MM-DD, the wake day).
func checkinDate(w http.ResponseWriter, r *http.Request) (string, bool) {
	date := r.PathValue("date")
	if t, err := time.Parse(dayLayout, date); err != nil || t.Format(dayLayout) != date {
		httpx.WriteError(w, 400, "date must be YYYY-MM-DD")
		return "", false
	}
	return date, true
}

// checkinProblem says what is wrong with a check-in report for the wake day date at now, or "" (422).
func checkinProblem(date string, in model.RecoveryCheckinPut, now time.Time, loc *time.Location) string {
	switch {
	case in.TZOffsetSec == nil || in.Soreness == nil:
		return "tz_offset_sec and soreness are required"
	case in.Bedtime.IsZero() || in.WakeTime.IsZero():
		return "bedtime and wake_time are required"
	case in.PerceivedSleepQuality < 1 || in.PerceivedSleepQuality > 10 || in.PerceivedFreshness < 1 || in.PerceivedFreshness > 10:
		return "perceived_sleep_quality and perceived_freshness must be 1 to 10"
	case *in.TZOffsetSec%60 != 0 || *in.TZOffsetSec < -maxTZOffsetSec || *in.TZOffsetSec > maxTZOffsetSec:
		return "tz_offset_sec must be a multiple of 60 between -50400 and 50400"
	}
	if d := in.WakeTime.Sub(in.Bedtime); d < checkinMinSleep || d > checkinMaxSleep {
		return "wake_time must be 1 to 16 hours after bedtime"
	}
	if in.WakeTime.After(now.Add(checkinFutureSlack)) {
		return "wake_time cannot be in the future"
	}
	if in.WakeTime.In(time.FixedZone("", *in.TZOffsetSec)).Format(dayLayout) != date {
		return "date must be the calendar date of wake_time at tz_offset_sec"
	}
	if !behavior.Editable(date, now.In(loc)) {
		return "a check-in can be saved for today and the 14 days before"
	}
	return ""
}

func (h *handler) putCheckin(w http.ResponseWriter, r *http.Request) {
	date, ok := checkinDate(w, r)
	if !ok {
		return
	}
	var in model.RecoveryCheckinPut
	if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	now := h.coachingNow()
	if msg := checkinProblem(date, in, now, loc); msg != "" {
		httpx.WriteError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	out, err := h.st.PutCheckin(r.Context(), userOf(r).ID, h.recoveryGrant(r), date, store.CheckinReport{
		Bedtime: in.Bedtime.UTC(), WakeTime: in.WakeTime.UTC(), TZOffsetSec: *in.TZOffsetSec,
		Quality: in.PerceivedSleepQuality, Freshness: in.PerceivedFreshness, Soreness: *in.Soreness,
	}, now, scoreCheckin)
	if h.recoveryConsentChanged(w, r, err) {
		return
	}
	if err != nil {
		recoveryFail(w, r, err)
		return
	}
	metrics.RecoveryWrites.WithLabelValues("checkin_saved").Inc()
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handler) getCheckin(w http.ResponseWriter, r *http.Request) {
	date, ok := checkinDate(w, r)
	if !ok {
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := h.st.Checkin(r.Context(), userOf(r).ID, loc, date)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "no check-in for that day")
	case err != nil:
		recoveryFail(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func (h *handler) deleteCheckin(w http.ResponseWriter, r *http.Request) {
	date, ok := checkinDate(w, r)
	if !ok {
		return
	}
	if err := h.st.DeleteCheckin(r.Context(), userOf(r).ID, date, h.coachingNow(), scoreCheckin); err != nil {
		recoveryFail(w, r, err)
		return
	}
	metrics.RecoveryWrites.WithLabelValues("checkin_deleted").Inc()
	w.WriteHeader(http.StatusNoContent)
}

// listCheckins is the range read, the /v1/days rules (D295): both ends optional (the 30 days ending today), 400 on a
// malformed date, 422 when from > to or the range is over 400 days. Oldest first, superseded ones included.
func (h *handler) listCheckins(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		writeRangeError(w, r, err)
		return
	}
	a, _ := time.Parse(dayLayout, from)
	b, _ := time.Parse(dayLayout, to)
	if b.Sub(a) >= checkinMaxRangeDays*24*time.Hour {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "the range is over 400 days")
		return
	}
	loc, err := h.location(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := h.st.Checkins(r.Context(), userOf(r).ID, loc, from, to)
	if err != nil {
		recoveryFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handler) recoveryStatus(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.RecoveryStatus(r.Context(), userOf(r).ID, h.coachingNow())
	if err != nil {
		recoveryFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// validRPE says whether v is a session RPE: 1 to 10 in steps of 0.5.
func validRPE(v float64) bool {
	return !math.IsNaN(v) && v >= 1 && v <= 10 && math.Mod(v*2, 1) == 0
}

func (h *handler) putWorkoutRPE(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, 400, "id must be a UUID")
		return
	}
	var in model.WorkoutRPEPut
	if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if in.SessionRPE == nil || !validRPE(*in.SessionRPE) {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "session_rpe must be 1 to 10 in steps of 0.5")
		return
	}
	out, err := h.st.PutWorkoutRating(r.Context(), userOf(r).ID, h.recoveryGrant(r), id, *in.SessionRPE)
	switch {
	case h.recoveryConsentChanged(w, r, err):
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "workout not found")
	case errors.Is(err, store.ErrHiddenDuplicate):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "this workout is hidden behind another recording of it: rate the primary")
	case err != nil:
		recoveryFail(w, r, err)
	default:
		metrics.RecoveryWrites.WithLabelValues("rpe_saved").Inc()
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func (h *handler) deleteWorkoutRPE(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, 400, "id must be a UUID")
		return
	}
	err = h.st.DeleteWorkoutRating(r.Context(), userOf(r).ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "workout not found")
	case err != nil:
		recoveryFail(w, r, err)
	default:
		metrics.RecoveryWrites.WithLabelValues("rpe_deleted").Inc()
		w.WriteHeader(http.StatusNoContent)
	}
}

// rescoreStaleCheckins rescores the caller's check-ins that an older model version computed (a model bump, D292):
// the next recompute does it. Best effort: a failure is logged without its error and never fails the recompute.
func (h *handler) rescoreStaleCheckins(r *http.Request) {
	if _, err := h.st.RescoreStaleCheckins(r.Context(), userOf(r).ID, scoring.CheckinModelVersion, h.coachingNow(), scoreCheckin); err != nil {
		h.log.WarnContext(r.Context(), "rescoring check-ins failed")
	}
}
