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
// synthetic line that must fail; the good lines are what the engine writes and
// must pass.
func TestEngineLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-23T07:38:47.3Z","level":"INFO","service":"scoring"`
	ctx := fmt.Sprintf(`"request_id":%q,"user_id":%q`, uuid.NewString(), uuid.NewString())
	line := func(msg, rest string) string {
		return `{` + stamp + `,"msg":"` + msg + `",` + ctx + `,` + rest + `}`
	}
	scoredOK := `"day":"2026-10-01","model_version":"` + ModelVersion + `","inputs_changed":true`

	good := []string{
		line("scored day", scoredOK),
		line("rescored days after a history change", `"days":3,"through":"2026-10-10"`),
		line("rescored days behind their history", `"days":7,"from":"2026-09-01"`),
		line("rescore after history change capped", `"after":"2026-10-01","days":400`),
		line("mark scored", `"day":"2026-10-01","err":"timeout"`),
		line("sleep baseline learned", `"applied":false`),
		// a line without a request, as the learner's has
		`{` + stamp + `,"msg":"sleep baseline learned","applied":true}`,
	}
	for _, g := range good {
		if _, problems := logcheck.Check(g, logLineKeys, engineLogKeys); len(problems) > 0 {
			t.Errorf("a real line is rejected: %s\n%v", g, problems)
		}
	}

	bad := map[string]string{
		"recovery under applied":        line("sleep baseline learned", `"applied":81`),
		"float under applied":           line("sleep baseline learned", `"applied":61.7`),
		"string under applied":          line("sleep baseline learned", `"applied":"true"`),
		"score under a renamed key":     line("sleep baseline learned", `"applied":true,"score":81`),
		"recovery under days":           line("rescored days behind their history", `"days":81.5,"from":"2026-09-01"`),
		"days over the engine's bound":  line("rescored days behind their history", `"days":401,"from":"2026-09-01"`),
		"negative days":                 line("rescored days behind their history", `"days":-3,"from":"2026-09-01"`),
		"score under from":              line("rescored days behind their history", `"days":3,"from":81`),
		"non-day through":               line("rescored days after a history change", `"days":3,"through":"yesterday"`),
		"timestamp as a day":            line("rescored days after a history change", `"days":3,"through":"2026-10-10T07:38:47Z"`),
		"impossible day":                line("scored day", strings.Replace(scoredOK, "2026-10-01", "2026-13-45", 1)),
		"day as a number":               line("scored day", strings.Replace(scoredOK, `"2026-10-01"`, `20261001`, 1)),
		"model_version not the build's": line("scored day", strings.Replace(scoredOK, ModelVersion, "61.7", 1)),
		"inputs_changed as a count":     line("scored day", strings.Replace(scoredOK, `true`, `1`, 1)),
		"hrv under a new key":           line("scored day", scoredOK+`,"hrv_ms":61.7`),
		"recovery under a new key":      line("scored day", scoredOK+`,"recovery":81`),
		"capped days as float":          line("rescore after history change capped", `"after":"2026-10-01","days":3.5`),
		"err as a number":               line("mark scored", `"day":"2026-10-01","err":47.3`),
		"unknown message":               line("recovery computed", `"day":"2026-10-01"`),
		"non-UUID user_id":              `{` + stamp + `,"msg":"scored day","user_id":"u-81",` + scoredOK + `}`,
		"score as user_id":              `{` + stamp + `,"msg":"scored day","user_id":81,` + scoredOK + `}`,
		"non-UUID request_id":           `{` + stamp + `,"msg":"scored day","request_id":"req-logs",` + scoredOK + `}`,
		"other service":                 strings.Replace(line("scored day", scoredOK), `"scoring"`, `"api"`, 1),
		"level outside the enum":        strings.Replace(line("scored day", scoredOK), `"INFO"`, `"81"`, 1),
		"time that is a value":          strings.Replace(line("scored day", scoredOK), `"2026-10-23T07:38:47.3Z"`, `"47.3"`, 1),
	}
	for name, l := range bad {
		if _, problems := logcheck.Check(l, logLineKeys, engineLogKeys); len(problems) == 0 {
			t.Errorf("%s: accepted: %s", name, l)
		}
	}
}
