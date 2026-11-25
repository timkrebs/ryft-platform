package main

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/nutrition"
)

const nutritionDiaryExportPath = "/v1/me/nutrition-export"

func (h *handler) exportNutritionDiary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		nutritionAnswer(w, r, 0, nil, nutrition.ErrInvalid)
		return
	}
	for key, values := range q {
		if (key != "after" && key != "limit") || len(values) != 1 {
			nutritionAnswer(w, r, 0, nil, nutrition.ErrInvalid)
			return
		}
	}
	var after uuid.UUID
	if q.Has("after") {
		after, err = uuid.Parse(q.Get("after"))
		if err != nil || after == uuid.Nil {
			nutritionAnswer(w, r, 0, nil, nutrition.ErrInvalid)
			return
		}
	}
	limit := 20
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			nutritionAnswer(w, r, 0, nil, nutrition.ErrInvalid)
			return
		}
	}
	page, err := h.st.ExportNutritionMeals(r.Context(), userOf(r).ID, after, limit)
	nutritionAnswer(w, r, http.StatusOK, page, err)
}
