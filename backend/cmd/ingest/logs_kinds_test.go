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
func TestSyncLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-23T07:38:47.3Z","level":"INFO","service":"ingest"`
	reqID, userID := uuid.NewString(), uuid.NewString()
	ctx := fmt.Sprintf(`"request_id":%q,"user_id":%q,"route":"POST /v1/sync"`, reqID, userID)
	line := func(msg, rest string) string {
		return `{` + stamp + `,"msg":"` + msg + `",` + ctx + `,` + rest + `}`
	}
	dev := uuid.NewString()
	syncOK := `"device":"` + dev + `","samples":3,"sleep":1,"workouts":1,"days":2`
	httpOK := `"method":"POST","path":"/v1/sync","status":200,"bytes":311,"dur_ms":4`
	forbid := logcheck.Forbidden{Texts: syncTexts, Numbers: syncNumbers}

	for _, g := range []string{
		line("sync", syncOK),
		line("http", httpOK),
		line("http", httpOK+`,"code":"invite_required","gate":"invite_required,consent_required"`),
		line("request failed", `"status":500,"err":"samples: ERROR: invalid byte sequence"`),
	} {
		logcheck.Accepts(t, syncStandardKeys, syncLogKeys, forbid, g)
	}

	key := logcheck.Key
	for _, m := range []struct{ name, line, want string }{
		{"float under a count", line("sync", strings.Replace(syncOK, `"samples":3`, `"samples":61.7`, 1)), key("samples")},
		{"negative count", line("sync", strings.Replace(syncOK, `"days":2`, `"days":-47`, 1)), key("days")},
		{"count as string", line("sync", strings.Replace(syncOK, `"sleep":1`, `"sleep":"517"`, 1)), key("sleep")},
		{"count as bool", line("sync", strings.Replace(syncOK, `"workouts":1`, `"workouts":true`, 1)), key("workouts")},
		{"a request's integer under a count", line("sync", strings.Replace(syncOK, `"samples":3`, `"samples":12345`, 1)), "value 12345"},
		{"score under a renamed key", line("sync", syncOK+`,"score":81`), key("score")},
		{"hrv under a new key", line("sync", syncOK+`,"hrv_ms":61.7`), key("hrv_ms")},
		{"non-UUID device", line("sync", strings.Replace(syncOK, `"device":"`+dev+`"`, `"device":"Tims iPhone"`, 1)), key("device")},
		{"float status", line("http", strings.Replace(httpOK, `"status":200`, `"status":47.3`, 1)), key("status")},
		{"status out of range", line("http", strings.Replace(httpOK, `"status":200`, `"status":81`, 1)), key("status")},
		{"float dur_ms", line("http", strings.Replace(httpOK, `"dur_ms":4`, `"dur_ms":4.5`, 1)), key("dur_ms")},
		{"method outside the enum", line("http", strings.Replace(httpOK, `"POST"`, `"61.7"`, 1)), key("method")},
		{"code outside the enum", line("http", httpOK+`,"code":"recovery_81"`), key("code")},
		{"gate outside the enum", line("http", httpOK+`,"gate":"invite_required,81"`), key("gate")},
		{"path with a query", line("http", strings.Replace(httpOK, `/v1/sync`, `/v1/sync?hrv=61.7`, 1)), key("path")},
		{"failed status below 500", line("request failed", `"status":61,"err":"x"`), key("status")},
		{"err as a number", line("request failed", `"status":500,"err":61.7`), key("err")},
		{"a request's text in err", line("request failed", `"status":500,"err":"samples: 61.7"`), `holds "61.7"`},
		{"unknown message", line("sync done", syncOK), "a new log line"},
		{"non-UUID user_id", `{` + stamp + `,"msg":"sync","user_id":"u-81",` + syncOK + `}`, key("user_id")},
		{"non-UUID request_id", `{` + stamp + `,"msg":"sync","request_id":"61.7",` + syncOK + `}`, key("request_id")},
		{"score as user_id", `{` + stamp + `,"msg":"sync","user_id":81,` + syncOK + `}`, key("user_id")},
		{"user_id twice, a score first", line("sync", `"user_id":81,`+syncOK), `key "user_id" twice`},
		{"other service", strings.Replace(line("sync", syncOK), `"service":"ingest"`, `"service":"api"`, 1), key("service")},
		{"level outside the enum", strings.Replace(line("sync", syncOK), `"INFO"`, `"81"`, 1), key("level")},
		{"time that is a value", strings.Replace(line("sync", syncOK), `"2026-10-23T07:38:47.3Z"`, `"47.3"`, 1), key("time")},
		{"msg is a number", `{` + stamp + `,"msg":61.7,` + syncOK + `}`, key("msg")},
		{"route is a value", strings.Replace(line("sync", syncOK), `"POST /v1/sync"`, `"61.7 ms"`, 1), key("route")},
	} {
		logcheck.Rejects(t, syncStandardKeys, syncLogKeys, forbid, m.name, m.line, m.want)
	}
}
