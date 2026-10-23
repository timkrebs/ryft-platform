package scoring

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/logx/logcheck"
)

// The checker has to reject what it is there for: a score under an allowed key
// (recovery as "applied"), a score under a renamed key. Each mutation is a
// synthetic line that must fail, for the key it mutates; the good lines are
// what the engine writes and must pass.
func TestEngineLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-23T07:38:47.3Z","level":"INFO","service":"scoring"`
	reqID, userID := uuid.NewString(), uuid.NewString()
	ctx := fmt.Sprintf(`"request_id":%q,"user_id":%q`, reqID, userID)
	line := func(msg, rest string) string {
		return `{` + stamp + `,"msg":"` + msg + `",` + ctx + `,` + rest + `}`
	}
	scoredOK := `"day":"2026-10-01","model_version":"` + ModelVersion + `","inputs_changed":true`
	forbid := logcheck.Forbidden{Texts: []string{"61.7"}, Numbers: []float64{517, 61.7, 80, 67}}

	for _, g := range []string{
		line("scored day", scoredOK),
		line("rescored days after a history change", `"days":3,"through":"2026-10-10"`),
		line("rescored days behind their history", `"days":7,"from":"2026-09-01"`),
		line("rescore after history change capped", `"after":"2026-10-01","days":400`),
		line("mark scored", `"day":"2026-10-01","err":"timeout"`),
		line("sleep baseline learned", `"applied":false`),
		// a line without a request, as the learner's has
		`{` + stamp + `,"msg":"sleep baseline learned","applied":true}`,
	} {
		logcheck.Accepts(t, logLineKeys, engineLogKeys, forbid, g)
	}

	key := logcheck.Key
	for _, m := range []struct{ name, line, want string }{
		{"recovery under applied", line("sleep baseline learned", `"applied":81`), key("applied")},
		{"float under applied", line("sleep baseline learned", `"applied":61.7`), key("applied")},
		{"string under applied", line("sleep baseline learned", `"applied":"true"`), key("applied")},
		{"score under a renamed key", line("sleep baseline learned", `"applied":true,"score":81`), key("score")},
		{"recovery under days", line("rescored days behind their history", `"days":81.5,"from":"2026-09-01"`), key("days")},
		{"days over the engine's bound", line("rescored days behind their history", `"days":401,"from":"2026-09-01"`), key("days")},
		{"negative days", line("rescored days behind their history", `"days":-3,"from":"2026-09-01"`), key("days")},
		// an integer in range passes the kind; the known values catch it
		{"a known recovery as days", line("rescored days behind their history", `"days":80,"from":"2026-09-01"`), "value 80"},
		{"a known sleep performance as days", line("rescore after history change capped", `"after":"2026-10-01","days":67`), "value 67"},
		{"score under from", line("rescored days behind their history", `"days":3,"from":81`), key("from")},
		{"non-day through", line("rescored days after a history change", `"days":3,"through":"yesterday"`), key("through")},
		{"timestamp as a day", line("rescored days after a history change", `"days":3,"through":"2026-10-10T07:38:47Z"`), key("through")},
		{"impossible day", line("scored day", strings.Replace(scoredOK, "2026-10-01", "2026-13-45", 1)), key("day")},
		{"day as a number", line("scored day", strings.Replace(scoredOK, `"2026-10-01"`, `20261001`, 1)), key("day")},
		{"model_version not the build's", line("scored day", strings.Replace(scoredOK, ModelVersion, "61.7", 1)), key("model_version")},
		{"inputs_changed as a count", line("scored day", strings.Replace(scoredOK, `true`, `1`, 1)), key("inputs_changed")},
		{"hrv under a new key", line("scored day", scoredOK+`,"hrv_ms":61.7`), key("hrv_ms")},
		{"recovery under a new key", line("scored day", scoredOK+`,"recovery":81`), key("recovery")},
		{"capped days as float", line("rescore after history change capped", `"after":"2026-10-01","days":3.5`), key("days")},
		{"err as a number", line("mark scored", `"day":"2026-10-01","err":47.3`), key("err")},
		{"unknown message", line("recovery computed", `"day":"2026-10-01"`), "a new log line"},
		{"non-UUID user_id", `{` + stamp + `,"msg":"scored day","user_id":"u-81",` + scoredOK + `}`, key("user_id")},
		{"score as user_id", `{` + stamp + `,"msg":"scored day","user_id":81,` + scoredOK + `}`, key("user_id")},
		{"user_id twice, a score first", line("scored day", `"user_id":61.7,`+scoredOK), `key "user_id" twice`},
		{"non-UUID request_id", `{` + stamp + `,"msg":"scored day","request_id":"req-logs",` + scoredOK + `}`, key("request_id")},
		{"other service", strings.Replace(line("scored day", scoredOK), `"scoring"`, `"api"`, 1), key("service")},
		{"level outside the enum", strings.Replace(line("scored day", scoredOK), `"INFO"`, `"81"`, 1), key("level")},
		{"time that is a value", strings.Replace(line("scored day", scoredOK), `"2026-10-23T07:38:47.3Z"`, `"47.3"`, 1), key("time")},
	} {
		logcheck.Rejects(t, logLineKeys, engineLogKeys, forbid, m.name, m.line, m.want)
	}
}
