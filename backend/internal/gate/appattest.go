package gate

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/appattest"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// AppAttestOptions configure AppAttest for one service.
type AppAttestOptions struct {
	Config  config.AppAttest // the mode, the exempt accounts and the ring
	Service string           // "api" or "ingest": the metric's label (the log line's comes from Log)
	// Exempt says whether a request's route never needs a token (api's list, docs/API.md →
	// Conventions → App Attest → Exempt); nil exempts no route (ingest).
	Exempt func(*http.Request) bool
	Log    *slog.Logger
	Now    func() time.Time // nil: time.Now
}

// deviceHeader is the install's id (docs/API.md → Conventions → Device; cmd/api's DeviceHeader).
const deviceHeader = "X-RYFT-Device-ID"

// msgAppAttestRequired is the 403's fixed text (AA4: it never holds a request's value).
const msgAppAttestRequired = "this request needs a valid app token (X-RYFT-App-Token)"

// appBuild is AA1's X-RYFT-App-Build: digits and up to three dots, at most 24 characters.
var appBuild = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// AppBuild is the request's X-RYFT-App-Build when it has AA1's form, else "" (ignored, never refused).
func AppBuild(r *http.Request) string {
	v := r.Header.Get(model.HeaderAppBuild)
	if len(v) > 24 || !appBuild.MatchString(v) {
		return ""
	}
	return v
}

// AppAttest checks the app token (X-RYFT-App-Token) of the requests it wraps, behind the bearer
// check and the per-user limit (it reads the auth.Principal) and before the beta and consent gates
// (D245, D247). Under off it is next itself: the header is never read. Under warn and on an exempt
// account or route passes; a valid token passes; any other request is counted
// (ryft_app_attest_refusals_total), written as one warn line "app attest", and under on answered
// 403 app_attest_required. Under warn nothing in the answer changes.
//
// The warn line is written on a context of its own, without the request's log fields (they carry
// user_id): it names no account, token, key id or device, only the request_id, added by hand.
func AppAttest(o AppAttestOptions, next http.Handler) http.Handler {
	mode := o.Config.Mode
	// The zero Config (a mux built without LoadAppAttest, as in tests) is off too: LoadAppAttest only
	// ever yields off, warn or on.
	if mode != config.ModeWarn && mode != config.ModeOn {
		return next
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Outside auth.Middleware there is no principal: uuid.Nil is no account's, so no token
		// is valid for it (fail closed).
		p, _ := auth.FromContext(r.Context())
		if o.Config.Exempt(p.UserID) || (o.Exempt != nil && o.Exempt(r)) {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get(model.HeaderAppToken)
		reason := appattest.Missing
		switch {
		case o.Config.Ring != nil:
			reason = o.Config.Ring.Check(token, p.UserID, now())
		case token != "":
			reason = appattest.Invalid // unreachable: config refuses warn and on without a ring
		}
		if reason == appattest.Valid {
			next.ServeHTTP(w, r)
			return
		}
		metrics.AppAttestRefusals.WithLabelValues(string(reason), o.Service).Inc()
		warnLine(o.Log, r, mode, reason)
		if mode == config.ModeOn {
			httpx.WriteCodedError(w, r, http.StatusForbidden, model.CodeAppAttestRequired, msgAppAttestRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// warnLine is decision 8's line. context.Background() carries no logx fields, so the handler adds
// no user_id, client_request_id or route; the request_id is the one value joined by hand. service
// is the logger's own.
func warnLine(log *slog.Logger, r *http.Request, mode config.Mode, reason appattest.Reason) {
	if log == nil {
		return
	}
	requestID := ""
	if f := httpx.Info(r.Context()); f != nil {
		requestID = f.RequestID
	}
	log.LogAttrs(context.Background(), slog.LevelWarn, "app attest",
		slog.String("code", model.CodeAppAttestRequired),
		slog.String("reason", string(reason)),
		slog.String("mode", string(mode)),
		slog.String("route_group", RouteGroup(r.URL.Path)),
		slog.String("method", r.Method),
		slog.String("app_build", AppBuild(r)),
		slog.Bool("app_device", r.Header.Get(deviceHeader) != ""), // whether it is there, never its value
		slog.String("request_id", requestID),
	)
}

// routeGroups is decision 8's bounded route_group by the first path segment after /v1/.
var routeGroups = map[string]string{
	"sync":     "sync",
	"sessions": "train", "plans": "train", "exercises": "train", "train": "train",
	"journal": "journal", "behaviors": "journal",
	"me": "account", "devices": "account",
	"config": "config", "model": "config",
	"days": "reads", "sleep": "reads", "workouts": "reads", "trends": "reads", "insights": "reads", "recompute": "reads",
}

// RouteGroup is the warn line's route_group of a path: sync, train, journal, account, config, reads or
// other; never the path itself (it can hold an id or a day).
func RouteGroup(path string) string {
	rest, ok := strings.CutPrefix(path, "/v1/")
	if !ok {
		return "other"
	}
	seg, _, _ := strings.Cut(rest, "/")
	if g, ok := routeGroups[seg]; ok {
		return g
	}
	return "other"
}
