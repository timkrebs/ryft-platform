package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/store"
)

const nutritionPrefix = "/v1/nutrition"

type nutritionAdvancedKey struct{}

// redactPrivatePath is the outermost wrapper of /v1/: it sets what the access line says for the path of the routes
// whose path holds a private value, before any middleware can reject the request (a 401, a 429, a gate, the mux's
// 405 and 404 all log the redacted path). Nutrition logs its prefix; the recovery routes log their pattern.
func redactPrivatePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logPath := privateLogPath(r.URL.Path); logPath != "" {
			if info := httpx.Info(r.Context()); info != nil {
				info.LogPath = logPath
			}
		}
		next.ServeHTTP(w, r)
	})
}

const (
	recoveryCheckinPrefix = "/v1/recovery/checkins/"
	workoutsPrefix        = "/v1/workouts/"
	workoutRPESuffix      = "/rpe"
)

// privateLogPath is what the access line logs instead of path, "" when the path holds nothing private.
func privateLogPath(path string) string {
	switch {
	case path == nutritionPrefix || strings.HasPrefix(path, nutritionPrefix+"/"):
		return nutritionPrefix
	case strings.HasPrefix(path, recoveryCheckinPrefix): // the wake date, whatever follows it
		return recoveryCheckinPrefix + "{date}"
	}
	// /v1/workouts/{id}/rpe, with or without a trailing slash, only: GET /v1/workouts/{id} keeps its path.
	if rest, ok := strings.CutPrefix(path, workoutsPrefix); ok {
		if id, ok := strings.CutSuffix(strings.TrimSuffix(rest, "/"), workoutRPESuffix); ok && id != "" && !strings.Contains(id, "/") {
			return workoutsPrefix + "{id}" + workoutRPESuffix
		}
	}
	return ""
}

func (h *handler) nutritionRoutes(mux *http.ServeMux) {
	register := func(method, path string, fn http.HandlerFunc) {
		mux.HandleFunc(method+" "+nutritionPrefix+path, h.nutritionAccess(false, fn))
	}
	advanced := func(method, path string, fn http.HandlerFunc) {
		mux.HandleFunc(method+" "+nutritionPrefix+path, h.nutritionGate(fn))
	}
	erasure := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc("DELETE "+nutritionPrefix+path, fn)
	}
	register("POST", "/foods/search", nutritionBody(http.StatusOK, h.searchNutritionFoods))
	register("POST", "/foods/barcode", nutritionBody(http.StatusOK, h.nutritionBarcode))
	register("GET", "/foods/{id}", h.nutritionFoodDetail)
	register("GET", "/foods/{id}/portions", nutritionByID(h.st.FoodPortions))
	register("PUT", "/foods/{id}/favorite", h.nutritionFoodFavorite(h.st.PutFoodFavorite))
	erasure("/foods/{id}/favorite", h.nutritionFoodFavorite(h.st.DeleteFoodFavorite))
	register("POST", "/foods/custom", nutritionBody(http.StatusOK, h.st.CreateCustomFood))
	advanced("POST", "/foods/submissions", nutritionBody(http.StatusOK, h.st.SubmitFood))
	register("POST", "/meals", h.createNutritionMeal)
	register("GET", "/meals", h.listNutritionMeals)
	register("GET", "/meals/{id}", nutritionByID(h.st.NutritionMeal))
	register("PATCH", "/meals/{id}", h.patchNutritionMeal)
	erasure("/meals/{id}", h.deleteNutritionMeal)
	register("GET", "/daily", h.dailyNutrition)
	register("GET", "/summary", h.dailyNutrition)
	advanced("PUT", "/glucose", h.putNutritionGlucose)
	erasure("/glucose", h.deleteNutritionGlucose)
	register("GET", "/goals", h.nutritionGoals)
	register("PUT", "/goals", nutritionBody(http.StatusOK, h.st.PutNutritionGoals))
	advanced("GET", "/n2r/profile", h.getN2RProfile)
	advanced("PUT", "/n2r/profile", h.n2rConsent(nutritionBody(http.StatusOK, h.st.PutN2RProfile)))
	erasure("/n2r/profile", h.deleteN2RProfile)
	advanced("GET", "/guidance/current", h.n2rConsent(h.currentN2RGuidance))
}

func (h *handler) nutritionGate(fn http.HandlerFunc) http.HandlerFunc {
	return h.nutritionAccess(true, fn)
}

func (h *handler) nutritionAccess(advancedOnly bool, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if info := httpx.Info(r.Context()); info != nil && r.Pattern != "" {
			info.LogPath = r.Pattern
		}
		user := userOf(r)
		all, err := h.st.Flags(r.Context())
		if err != nil {
			nutritionAnswer(w, r, 0, nil, err)
			return
		}
		pilot := flags.Enabled(all, model.FlagNutrition, user.ID)
		if !pilot && (advancedOnly || !flags.Enabled(all, model.FlagNutritionDiary, user.ID)) {
			httpx.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			switch r.URL.Path {
			case nutritionPrefix + "/foods/search", nutritionPrefix + "/foods/barcode":
			default:
				consent := h.beta
				if consent.Consent == config.ModeWarn {
					consent.Consent = config.ModeOn
				}
				if !gate.Consent(w, r, consent, user, "api") {
					return
				}
			}
		}
		if !advancedOnly {
			r = r.WithContext(context.WithValue(r.Context(), nutritionAdvancedKey{}, pilot))
		}
		fn(w, r)
	}
}

func nutritionAnswer(w http.ResponseWriter, r *http.Request, status int, body any, err error) {
	switch {
	case err == nil:
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return
		}
		httpx.WriteJSON(w, status, body)
	case errors.Is(err, nutrition.ErrInvalid):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "invalid nutrition input")
	case errors.Is(err, nutrition.ErrConversion):
		httpx.WriteError(w, http.StatusUnprocessableEntity, nutrition.ErrConversion.Error())
	case errors.Is(err, store.ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, store.ErrNutritionConflict):
		httpx.WriteError(w, http.StatusConflict, "nutrition revision or request conflicts with a saved record")
	default:
		// PostgreSQL errors can contain private field values in constraint detail.
		httpx.Fail(w, r, errors.New("nutrition store operation failed"))
	}
}

func nutritionBody[I, O any](status int, fn func(context.Context, uuid.UUID, I) (O, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in I
		if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
			httpx.WriteDecodeError(w, err)
			return
		}
		out, err := fn(r.Context(), userOf(r).ID, in)
		nutritionAnswer(w, r, status, out, err)
	}
}

func nutritionByID[O any](fn func(context.Context, uuid.UUID, uuid.UUID) (O, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil || id == uuid.Nil {
			httpx.NotFound(w, r)
			return
		}
		out, err := fn(r.Context(), userOf(r).ID, id)
		nutritionAnswer(w, r, http.StatusOK, out, err)
	}
}

func (h *handler) nutritionFoodDetail(w http.ResponseWriter, r *http.Request) {
	nutritionByID(func(ctx context.Context, user, id uuid.UUID) (model.FoodDetail, error) {
		return h.st.FoodForLanguage(ctx, user, id, r.URL.Query().Get("language"))
	})(w, r)
}

func (h *handler) nutritionFoodFavorite(write func(context.Context, uuid.UUID, uuid.UUID) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil || id == uuid.Nil {
			httpx.NotFound(w, r)
			return
		}
		nutritionAnswer(w, r, http.StatusNoContent, nil, write(r.Context(), userOf(r).ID, id))
	}
}

func (h *handler) searchNutritionFoods(ctx context.Context, user uuid.UUID, in model.FoodSearch) (model.FoodSearchPage, error) {
	out, err := h.st.SearchFoods(ctx, user, in)
	if err == nil {
		metrics.NutritionSearchResults.Observe(float64(len(out.Foods)))
	}
	return out, err
}

func (h *handler) nutritionBarcode(ctx context.Context, user uuid.UUID, in model.FoodBarcodeQuery) (model.FoodBarcodeResult, error) {
	out, err := h.st.LookupFoodBarcode(ctx, user, in)
	if err == nil {
		result := "miss"
		switch {
		case out.Conflicted:
			result = "conflict"
		case len(out.Matches) > 0:
			result = "hit"
		}
		metrics.NutritionBarcode.WithLabelValues(result).Inc()
	}
	return out, err
}

func (h *handler) createNutritionMeal(w http.ResponseWriter, r *http.Request) {
	var in model.NutritionMealInput
	if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	out, created, err := h.st.CreateNutritionMeal(r.Context(), userOf(r).ID, in)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	nutritionAnswer(w, r, status, out, err)
}

func (h *handler) listNutritionMeals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := 20, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if q.Has(key) {
			n, err := strconv.Atoi(q.Get(key))
			if err != nil {
				nutritionAnswer(w, r, 0, nil, nutrition.ErrInvalid)
				return
			}
			*target = n
		}
	}
	out, err := h.st.NutritionMeals(r.Context(), userOf(r).ID, q.Get("from"), q.Get("to"), limit, offset)
	nutritionAnswer(w, r, http.StatusOK, out, err)
}

func (h *handler) patchNutritionMeal(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id == uuid.Nil {
		httpx.NotFound(w, r)
		return
	}
	var in model.NutritionMealPatch
	if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	out, err := h.st.PatchNutritionMeal(r.Context(), userOf(r).ID, id, in)
	nutritionAnswer(w, r, http.StatusOK, out, err)
}

func (h *handler) deleteNutritionMeal(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id == uuid.Nil {
		httpx.NotFound(w, r)
		return
	}
	err = h.st.DeleteNutritionMeal(r.Context(), userOf(r).ID, id)
	nutritionAnswer(w, r, http.StatusNoContent, nil, err)
}

func (h *handler) dailyNutrition(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if r.URL.Path == nutritionPrefix+"/daily" {
		from = q.Get("date")
		to = from
	}
	var penalty bool
	if q.Has("chrononutrition") {
		var err error
		penalty, err = strconv.ParseBool(q.Get("chrononutrition"))
		if err != nil {
			nutritionAnswer(w, r, 0, nil, nutrition.ErrInvalid)
			return
		}
	}
	var out []model.NutritionDaily
	var err error
	if pilot, _ := r.Context().Value(nutritionAdvancedKey{}).(bool); pilot {
		out, err = h.st.NutritionDailyWithN2R(r.Context(), userOf(r).ID, from, to, penalty, time.Now(), h.beta.PrivacyPolicyVersion)
	} else {
		out, err = h.st.NutritionDiaryTotals(r.Context(), userOf(r).ID, from, to)
	}
	if err != nil {
		nutritionAnswer(w, r, 0, nil, err)
	} else if r.URL.Path == nutritionPrefix+"/daily" {
		httpx.WriteJSON(w, http.StatusOK, out[0])
	} else {
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func (h *handler) n2rConsent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		consent := h.beta
		if consent.PrivacyPolicyVersion != "" {
			consent.Consent = config.ModeOn
		}
		if gate.Consent(w, r, consent, userOf(r), "api") {
			next(w, r)
		}
	}
}

func (h *handler) getN2RProfile(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.N2RProfile(r.Context(), userOf(r).ID)
	nutritionAnswer(w, r, http.StatusOK, out, err)
}

func (h *handler) deleteN2RProfile(w http.ResponseWriter, r *http.Request) {
	err := h.st.DeleteN2RProfile(r.Context(), userOf(r).ID)
	nutritionAnswer(w, r, http.StatusNoContent, nil, err)
}

func (h *handler) currentN2RGuidance(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.N2RGuidance(r.Context(), userOf(r).ID, time.Now())
	nutritionAnswer(w, r, http.StatusOK, out, err)
}

func (h *handler) refreshN2R(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	all, err := h.st.Flags(ctx)
	if err != nil {
		return errors.New("read nutrition rollout")
	}
	var active bool
	for _, f := range all {
		active = active || f.Key == model.FlagNutrition && f.Enabled && (f.RolloutPct > 0 || len(f.AllowUserIDs) > 0)
	}
	if !active {
		return nil
	}
	err = h.st.RefreshN2RNights(ctx, time.Now(), h.beta.PrivacyPolicyVersion, func(user uuid.UUID) bool { return flags.Enabled(all, model.FlagNutrition, user) })
	if err != nil {
		return errors.New("nutrition calibration pass failed")
	}
	return nil
}

func (h *handler) nutritionGoals(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.NutritionGoals(r.Context(), userOf(r).ID)
	nutritionAnswer(w, r, http.StatusOK, out, err)
}

func (h *handler) putNutritionGlucose(w http.ResponseWriter, r *http.Request) {
	var in model.NutritionGlucoseUpload
	if err := httpx.ReadSingleJSON(w, r, &in, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	err := h.st.PutNutritionGlucose(r.Context(), userOf(r).ID, in.Samples)
	nutritionAnswer(w, r, http.StatusNoContent, nil, err)
}

func (h *handler) deleteNutritionGlucose(w http.ResponseWriter, r *http.Request) {
	err := h.st.DeleteNutritionGlucose(r.Context(), userOf(r).ID)
	nutritionAnswer(w, r, http.StatusNoContent, nil, err)
}
