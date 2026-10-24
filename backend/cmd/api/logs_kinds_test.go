package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx/logcheck"
)

// The checker has to reject what it is there for: a health value under an
// allowed key, a score under a renamed key. Each mutation is a synthetic line
// that must fail, for the key it mutates; the good lines are what the real code
// writes and must pass.
func TestWriteLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-23T07:38:47.3Z","level":"INFO","service":"api"`
	ctx := fmt.Sprintf(`"request_id":%q,"user_id":%q,"route":"POST /v1/feedback"`, uuid.NewString(), uuid.NewString())
	line := func(msg, rest string) string {
		return `{` + stamp + `,"msg":"` + msg + `",` + ctx + `,` + rest + `}`
	}
	fbOK := `"feedback_id":12,"category":"data","request_ids":1`
	httpOK := `"method":"PUT","path":"/v1/journal/2026-10-22","status":200,"bytes":311,"dur_ms":4`
	forbid := logcheck.Forbidden{Texts: writeTexts, Numbers: writeNumbers}

	for _, g := range []string{
		line("feedback received", fbOK),
		line("http", httpOK),
		line("http", httpOK+`,"code":"consent_required"`),
		line("request failed", `"status":500,"err":"closed pool"`),
	} {
		logcheck.Accepts(t, writeStandardKeys, writeLogKeys, forbid, g)
	}

	key := logcheck.Key
	for _, m := range []struct{ name, line, want string }{
		{"float under request_ids", line("feedback received", strings.Replace(fbOK, `"request_ids":1`, `"request_ids":61.7`, 1)), key("request_ids")},
		{"request_ids over the limit", line("feedback received", strings.Replace(fbOK, `"request_ids":1`, `"request_ids":21`, 1)), key("request_ids")},
		{"negative count", line("feedback received", strings.Replace(fbOK, `"request_ids":1`, `"request_ids":-1`, 1)), key("request_ids")},
		{"a request's integer as feedback_id", line("feedback received", strings.Replace(fbOK, `"feedback_id":12`, `"feedback_id":617`, 1)), "value 617"},
		{"float feedback_id", line("feedback received", strings.Replace(fbOK, `"feedback_id":12`, `"feedback_id":12.5`, 1)), key("feedback_id")},
		{"category outside the enum", line("feedback received", strings.Replace(fbOK, `"data"`, `"hrv 61.7"`, 1)), key("category")},
		{"category as a number", line("feedback received", strings.Replace(fbOK, `"data"`, `61`, 1)), key("category")},
		{"message under a new key", line("feedback received", fbOK+`,"message":"bad night"`), key("message")},
		{"the message's text under a listed key", line("feedback received", strings.Replace(fbOK, `"data"`, `"bad night"`, 1)), `holds "bad night"`},
		{"score under a renamed key", line("feedback received", fbOK+`,"score":81`), key("score")},
		{"value under an access key", line("http", httpOK+`,"value":61.7`), key("value")},
		{"float status", line("http", strings.Replace(httpOK, `"status":200`, `"status":47.3`, 1)), key("status")},
		{"float bytes", line("http", strings.Replace(httpOK, `"bytes":311`, `"bytes":61.7`, 1)), key("bytes")},
		{"path with a query", line("http", strings.Replace(httpOK, `2026-10-22`, `2026-10-22?hrv=61.7`, 1)), key("path")},
		{"code outside the enum", line("http", httpOK+`,"code":"recovery_81"`), key("code")},
		{"gate as a bool", line("http", httpOK+`,"gate":true`), key("gate")},
		{"method outside the enum", line("http", strings.Replace(httpOK, `"PUT"`, `"61.7"`, 1)), key("method")},
		{"failed status below 500", line("request failed", `"status":81,"err":"x"`), key("status")},
		{"err as a number", line("request failed", `"status":500,"err":617`), key("err")},
		{"unknown message", line("journal stored", `"entries":1`), "a new log line"},
		{"non-UUID user_id", `{` + stamp + `,"msg":"feedback received","user_id":"u-81",` + fbOK + `}`, key("user_id")},
		{"score as user_id", `{` + stamp + `,"msg":"feedback received","user_id":81,` + fbOK + `}`, key("user_id")},
		{"non-UUID request_id", `{` + stamp + `,"msg":"feedback received","request_id":"61.7",` + fbOK + `}`, key("request_id")},
		{"user_id twice, a score first", line("feedback received", `"user_id":61.7,`+fbOK), `key "user_id" twice`},
		{"other service", strings.Replace(line("feedback received", fbOK), `"service":"api"`, `"service":"scoring"`, 1), key("service")},
		{"level outside the enum", strings.Replace(line("feedback received", fbOK), `"INFO"`, `"81"`, 1), key("level")},
		{"time that is a value", strings.Replace(line("feedback received", fbOK), `"2026-10-23T07:38:47.3Z"`, `47.3`, 1), key("time")},
		{"client_request_id with spaces", `{` + stamp + `,"msg":"feedback received","client_request_id":"hrv 61.7",` + fbOK + `}`, key("client_request_id")},
	} {
		logcheck.Rejects(t, writeStandardKeys, writeLogKeys, forbid, m.name, m.line, m.want)
	}
}
