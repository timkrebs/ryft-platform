package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// userStore is what ensureUser reads: the user row and the smoke binding (*store.Store; a fake in tests).
type userStore interface {
	EnsureUser(ctx context.Context, id store.Identity) (model.User, error)
	IsSmokeAccount(ctx context.Context, id uuid.UUID) (bool, error)
}

// smokeRoutes is every request the smoke account's token may make to the api (D286), in one place:
// exactly what scripts/smoke_prod.sh and scripts/lib/smoke_auth.py send with it. Anything else,
// DELETE /v1/me, the export, the merge, the training import and nutrition included, is a 403
// smoke_route. Methods and paths match exactly; only {day} is a pattern (one path segment).
// Change it together with the script.
var smokeRoutes = []struct{ method, path string }{
	{http.MethodGet, "/v1/me"},
	{http.MethodPost, "/v1/me/invite"},
	{http.MethodGet, "/v1/config"},
	{http.MethodPost, "/v1/me/consent"},
	{http.MethodDelete, "/v1/me/consent"},
	{http.MethodGet, "/v1/model"},
	{http.MethodGet, "/v1/me/connections"},
	{http.MethodGet, "/v1/days"},
	{http.MethodGet, "/v1/days/{day}"},
}

// smokeAllow is auth.Options.SmokeAllow for the api: the request is on smokeRoutes.
func smokeAllow(r *http.Request) bool {
	for _, a := range smokeRoutes {
		if r.Method != a.method {
			continue
		}
		if r.URL.Path == a.path {
			return true
		}
		if strings.HasSuffix(a.path, "/{day}") {
			if day, ok := strings.CutPrefix(r.URL.Path, strings.TrimSuffix(a.path, "{day}")); ok && day != "" && !strings.Contains(day, "/") {
				return true
			}
		}
	}
	return false
}
