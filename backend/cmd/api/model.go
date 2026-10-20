package main

import (
	"net/http"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
)

// modelInfo is GET /v1/model: the scoring model and its changelog, the summary
// in the request's language. The changelog is embedded, so it cannot change
// while the process runs.
func (h *handler) modelInfo(w http.ResponseWriter, r *http.Request) {
	changes, err := scoring.Changelog()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	de := german(r)
	out := model.ModelInfo{Current: scoring.ModelVersion, Changelog: make([]model.ModelChangeEntry, 0, len(changes))}
	for _, c := range changes {
		e := model.ModelChangeEntry{Version: c.Version, Date: c.Date, Summary: c.SummaryEN, SummaryEN: c.SummaryEN,
			SummaryDE: c.SummaryDE, Affects: c.Affects}
		if de {
			e.Summary = c.SummaryDE
		}
		out.Changelog = append(out.Changelog, e)
	}
	httpx.WriteJSON(w, 200, out)
}
