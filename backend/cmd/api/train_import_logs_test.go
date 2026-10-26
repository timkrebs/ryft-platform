package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// importLogKeys is every line the training import and the Train routes around it may write
// with its keys and the kind of each value (docs/OPERATIONS.md → Data protection (logs),
// D117): the import's one line holds its result and counts, not the source (N6).
var importLogKeys = logcheck.Lines{
	"http": logcheck.HTTP(model.CodeInviteRequired, model.CodeConsentRequired, model.CodeImportInProgress,
		model.CodeImportNotEnabled),
	"request failed":            logcheck.RequestFailed(),
	"client closed the request": {"err": logcheck.Text(1024)},
	"train import": {
		"result": logcheck.Enum("ok", "timeout", "error"),
		"plans":  logcheck.CountMax(train.MaxImportPlans), "sessions": logcheck.CountMax(train.MaxImportSessions),
		"created": logcheck.CountMax(train.MaxImportPlans + train.MaxImportSessions), "unchanged": logcheck.CountMax(train.MaxImportPlans + train.MaxImportSessions),
		"updated": logcheck.CountMax(train.MaxImportPlans + train.MaxImportSessions), "rejected": logcheck.CountMax(train.MaxImportPlans + train.MaxImportSessions),
		"days": logcheck.Count, "duration_ms": logcheck.Count,
	},
}

// checkImportLog fails on a line, key or kind of value importLogKeys does not allow and on
// what the requests carried; it returns the lines by message.
func checkImportLog(t *testing.T, out string, texts []string, numbers []float64) map[string][]map[string]any {
	t.Helper()
	lines, problems := logcheck.Check(out, logcheck.Standard("api"), importLogKeys, logcheck.Forbidden{Texts: texts, Numbers: numbers})
	for _, p := range problems {
		t.Error(p)
	}
	return lines
}

// The checker rejects what it is there for on the import's line: a value under a count,
// a title under the source, a new key.
func TestImportLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-26T07:38:47.3Z","level":"INFO","service":"api"`
	ctx := fmt.Sprintf(`"request_id":%q,"user_id":%q,"route":"POST /v1/train/import"`, uuid.NewString(), uuid.NewString())
	line := func(rest string) string { return `{` + stamp + `,"msg":"train import",` + ctx + `,` + rest + `}` }
	ok := `"result":"ok","plans":1,"sessions":2,"created":2,"unchanged":0,"updated":0,"rejected":1,"days":1,"duration_ms":40`
	forbid := logcheck.Forbidden{Texts: []string{"Leg day"}, Numbers: []float64{123.25}}
	logcheck.Accepts(t, logcheck.Standard("api"), importLogKeys, forbid, line(ok))
	key := logcheck.Key
	for _, m := range []struct{ name, line, want string }{
		{"a weight under a count", line(strings.Replace(ok, `"created":2`, `"created":123.25`, 1)), key("created")},
		{"more sessions than a request holds", line(strings.Replace(ok, `"sessions":2`, `"sessions":51`, 1)), key("sessions")},
		{"the source again", line(ok + `,"source":"alpha_progression"`), key("source")},
		{"a result outside the enum", line(strings.Replace(ok, `"result":"ok"`, `"result":"busy"`, 1)), key("result")},
		{"session ids", line(ok + `,"session_ids":["` + uuid.NewString() + `"]`), key("session_ids")},
		{"a title under a new key", line(ok + `,"title":"Leg day"`), key("title")},
	} {
		logcheck.Rejects(t, logcheck.Standard("api"), importLogKeys, forbid, m.name, m.line, m.want)
	}
}

func ptrTo[T any](v T) *T { return &v }

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
