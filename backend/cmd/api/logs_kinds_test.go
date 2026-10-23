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
// that must fail; the good lines are what the real code writes and must pass.
func TestWriteLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-23T07:38:47.3Z","level":"INFO","service":"api"`
	ctx := fmt.Sprintf(`"request_id":%q,"user_id":%q,"route":"POST /v1/feedback"`, uuid.NewString(), uuid.NewString())
	line := func(msg, rest string) string {
		return `{` + stamp + `,"msg":"` + msg + `",` + ctx + `,` + rest + `}`
	}
	fbOK := `"feedback_id":12,"category":"data","request_ids":1`
	httpOK := `"method":"PUT","path":"/v1/journal/2026-10-22","status":200,"bytes":311,"dur_ms":4`

	good := []string{
		line("feedback received", fbOK),
		line("http", httpOK),
		line("http", httpOK+`,"code":"consent_required"`),
		line("request failed", `"status":500,"err":"closed pool"`),
	}
	for _, g := range good {
		if _, problems := logcheck.Check(g, writeStandardKeys, writeLogKeys); len(problems) > 0 {
			t.Errorf("a real line is rejected: %s\n%v", g, problems)
		}
	}

	bad := map[string]string{
		"float under request_ids":       line("feedback received", strings.Replace(fbOK, `"request_ids":1`, `"request_ids":61.7`, 1)),
		"request_ids over the limit":    line("feedback received", strings.Replace(fbOK, `"request_ids":1`, `"request_ids":617`, 1)),
		"negative count":                line("feedback received", strings.Replace(fbOK, `"request_ids":1`, `"request_ids":-1`, 1)),
		"float feedback_id":             line("feedback received", strings.Replace(fbOK, `"feedback_id":12`, `"feedback_id":12.5`, 1)),
		"category outside the enum":     line("feedback received", strings.Replace(fbOK, `"data"`, `"hrv 61.7"`, 1)),
		"category as a number":          line("feedback received", strings.Replace(fbOK, `"data"`, `61`, 1)),
		"message under a new key":       line("feedback received", fbOK+`,"message":"bad night"`),
		"score under a renamed key":     line("feedback received", fbOK+`,"score":81`),
		"value under an access key":     line("http", httpOK+`,"value":61.7`),
		"float status":                  line("http", strings.Replace(httpOK, `"status":200`, `"status":47.3`, 1)),
		"float bytes":                   line("http", strings.Replace(httpOK, `"bytes":311`, `"bytes":61.7`, 1)),
		"path with a query":             line("http", strings.Replace(httpOK, `2026-10-22`, `2026-10-22?hrv=61.7`, 1)),
		"code outside the enum":         line("http", httpOK+`,"code":"recovery_81"`),
		"gate as a bool":                line("http", httpOK+`,"gate":true`),
		"method outside the enum":       line("http", strings.Replace(httpOK, `"PUT"`, `"61.7"`, 1)),
		"failed status below 500":       line("request failed", `"status":81,"err":"x"`),
		"err as a number":               line("request failed", `"status":500,"err":617`),
		"unknown message":               line("journal stored", `"entries":1`),
		"non-UUID user_id":              `{` + stamp + `,"msg":"feedback received","user_id":"u-81",` + fbOK + `}`,
		"score as user_id":              `{` + stamp + `,"msg":"feedback received","user_id":81,` + fbOK + `}`,
		"non-UUID request_id":           `{` + stamp + `,"msg":"feedback received","request_id":"61.7",` + fbOK + `}`,
		"other service":                 strings.Replace(line("feedback received", fbOK), `"service":"api"`, `"service":"scoring"`, 1),
		"level outside the enum":        strings.Replace(line("feedback received", fbOK), `"INFO"`, `"81"`, 1),
		"time that is a value":          strings.Replace(line("feedback received", fbOK), `"2026-10-23T07:38:47.3Z"`, `47.3`, 1),
		"client_request_id with spaces": `{` + stamp + `,"msg":"feedback received","client_request_id":"hrv 61.7",` + fbOK + `}`,
	}
	for name, l := range bad {
		if _, problems := logcheck.Check(l, writeStandardKeys, writeLogKeys); len(problems) == 0 {
			t.Errorf("%s: accepted: %s", name, l)
		}
	}
}
