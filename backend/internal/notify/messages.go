// Package notify decides what RYFT pushes, to whom and when (cmd/notify runs
// it): the morning score, a scored strength session or its records, and a
// reminder when no data has arrived (docs/DECISIONS.md D70–D73). Messages are
// APNs payloads with localisation keys the app resolves (docs/API.md → Push);
// the arguments are formatted for the install's language.
package notify

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Message is one alert before it becomes a payload.
type Message struct {
	Category  string
	Day       string
	TitleKey  string
	TitleArgs []string
	BodyKey   string
	BodyArgs  []string
	URL       string // universal link the notification opens; empty: the app
	Expires   time.Time
}

// Payload is the APNs body: the alert as localisation keys and arguments, the
// category (its actions), mutable-content so the app's service extension can
// attach the ring, and RYFT's own keys.
func (m Message) Payload() []byte {
	ryft := map[string]string{"category": m.Category, "day": m.Day}
	if m.URL != "" {
		ryft["url"] = m.URL
	}
	b, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{
				"title-loc-key": m.TitleKey, "title-loc-args": nonNil(m.TitleArgs),
				"loc-key": m.BodyKey, "loc-args": nonNil(m.BodyArgs),
			},
			"sound": "default", "category": m.Category, "thread-id": m.Category, "mutable-content": 1,
		},
		"ryft": ryft,
	})
	return b
}

// SilentPayload wakes the app to refresh day in the background.
func SilentPayload(day string) []byte {
	b, _ := json.Marshal(map[string]any{"aps": map[string]any{"content-available": 1},
		"ryft": map[string]string{"kind": "refresh", "day": day}})
	return b
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Language is the catalog language for a stored locale (en unless de).
func Language(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "de") {
		return "de"
	}
	return "en"
}

// pct formats a score: "82%" in English, "82 %" in German.
func pct(v int, lang string) string {
	if lang == "de" {
		return strconv.Itoa(v) + " %"
	}
	return strconv.Itoa(v) + "%"
}

// decimal formats one decimal (whole numbers without), with a decimal comma in German.
func decimal(v float64, lang string) string {
	s := strings.TrimSuffix(strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64), ".0")
	if lang == "de" {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

const site = "https://ryftapp.de"

// Morning is MORNING_SCORE: "Recovery 82%" and the readiness sentence for
// its state. It expires at the end of the local day.
func Morning(day string, recovery int, readiness *int, state string, loc *time.Location, lang string) Message {
	r := recovery
	if readiness != nil {
		r = *readiness
	}
	key := map[string]string{model.StateBalanced: "MORNING_SCORE_BODY_BALANCED", model.StateStrained: "MORNING_SCORE_BODY_STRAINED",
		model.StateDepleted: "MORNING_SCORE_BODY_DEPLETED"}[state]
	if key == "" {
		key = "MORNING_SCORE_BODY_CALIBRATING"
	}
	end, _ := time.ParseInLocation("2006-01-02", day, loc)
	return Message{Category: model.NotifyMorningScore, Day: day,
		TitleKey: "MORNING_SCORE_TITLE", TitleArgs: []string{pct(recovery, lang)},
		BodyKey: key, BodyArgs: []string{strconv.Itoa(r)},
		URL: site + "/day/" + day, Expires: end.AddDate(0, 0, 1)}
}

// Session is SESSION_SCORED: the session's title, its strain and how many
// records it set.
func Session(day, sessionID, title string, strain float64, records int, now time.Time, lang string) Message {
	m := Message{Category: model.NotifySessionScored, Day: day, TitleKey: "SESSION_SCORED_TITLE", TitleArgs: []string{title},
		BodyKey: "SESSION_SCORED_BODY", BodyArgs: []string{decimal(strain, lang)},
		URL: site + "/train/session/" + sessionID, Expires: now.Add(12 * time.Hour)}
	if strings.TrimSpace(title) == "" {
		m.TitleKey, m.TitleArgs = "SESSION_SCORED_TITLE_UNTITLED", nil
	}
	switch {
	case records == 1:
		m.BodyKey = "SESSION_SCORED_BODY_PR"
	case records > 1:
		m.BodyKey, m.BodyArgs = "SESSION_SCORED_BODY_PRS", append(m.BodyArgs, strconv.Itoa(records))
	}
	return m
}

// Record is PR: the session's best record (sent instead of SESSION_SCORED
// when the user wants records but not session summaries).
func Record(day, sessionID, exercise, kind string, value float64, now time.Time, lang string) Message {
	key := map[string]string{model.PRE1RM: "PR_BODY_E1RM", model.PRMaxWeight: "PR_BODY_MAX_WEIGHT",
		model.PRMaxReps: "PR_BODY_MAX_REPS", model.PRMaxVolume: "PR_BODY_MAX_VOLUME"}[kind]
	v := decimal(value, lang)
	if kind == model.PRMaxReps {
		v = strconv.Itoa(int(math.Round(value)))
	}
	return Message{Category: model.NotifyPR, Day: day, TitleKey: "PR_TITLE", TitleArgs: []string{exercise},
		BodyKey: key, BodyArgs: []string{v}, URL: site + "/train/session/" + sessionID, Expires: now.Add(12 * time.Hour)}
}

// Stale is SYNC_STALE: no data for hours.
func Stale(day string, hours int, now time.Time) Message {
	return Message{Category: model.NotifySyncStale, Day: day, TitleKey: "SYNC_STALE_TITLE",
		BodyKey: "SYNC_STALE_BODY", BodyArgs: []string{strconv.Itoa(hours)}, Expires: now.Add(6 * time.Hour)}
}

// Catalog is every key's text in English and German, as the app ships them in
// Localizable.xcstrings (docs/API.md → Push). The server sends only keys and
// arguments; the catalog pins what each key says and how many arguments it takes.
var Catalog = map[string][2]string{
	"MORNING_SCORE_TITLE":            {"Recovery %@", "Erholung %@"},
	"MORNING_SCORE_BODY_BALANCED":    {"Readiness %@. You're ready to perform: a good day to push.", "Bereitschaft %@. Du bist bereit: ein guter Tag für hohe Belastung."},
	"MORNING_SCORE_BODY_STRAINED":    {"Readiness %@. Keep the load moderate today.", "Bereitschaft %@. Belaste dich heute moderat."},
	"MORNING_SCORE_BODY_DEPLETED":    {"Readiness %@. Take it easy today and protect tonight's sleep.", "Bereitschaft %@. Geh es heute ruhig an und schütze deinen Schlaf."},
	"MORNING_SCORE_BODY_CALIBRATING": {"Readiness %@. RYFT is still learning your baselines.", "Bereitschaft %@. RYFT lernt noch deine Ausgangswerte."},
	"SESSION_SCORED_TITLE":           {"%@ scored", "%@ ausgewertet"},
	"SESSION_SCORED_TITLE_UNTITLED":  {"Workout scored", "Training ausgewertet"},
	"SESSION_SCORED_BODY":            {"Strain %@.", "Belastung %@."},
	"SESSION_SCORED_BODY_PR":         {"Strain %@, 1 new record.", "Belastung %@, 1 neuer Rekord."},
	"SESSION_SCORED_BODY_PRS":        {"Strain %1$@, %2$@ new records.", "Belastung %1$@, %2$@ neue Rekorde."},
	"PR_TITLE":                       {"New record: %@", "Neuer Rekord: %@"},
	"PR_BODY_E1RM":                   {"Estimated one-rep max %@ kg.", "Geschätztes 1RM %@ kg."},
	"PR_BODY_MAX_WEIGHT":             {"%@ kg, your heaviest set yet.", "%@ kg, dein schwerster Satz bisher."},
	"PR_BODY_MAX_REPS":               {"%@ reps in one set, your most yet.", "%@ Wiederholungen in einem Satz, so viele wie nie."},
	"PR_BODY_MAX_VOLUME":             {"%@ kg in one set (reps × kg), your best yet.", "%@ kg in einem Satz (Wdh. × kg), dein Bestwert."},
	"SYNC_STALE_TITLE":               {"No new data", "Keine neuen Daten"},
	"SYNC_STALE_BODY":                {"Nothing new from your watch for %@ hours. Open RYFT to sync.", "Seit %@ Stunden nichts Neues von deiner Uhr. Öffne RYFT zum Synchronisieren."},
}

// Render fills a key's text the way iOS does (%@ and %n$@), for tests and docs.
func Render(key string, args []string, lang string) string {
	texts, ok := Catalog[key]
	if !ok {
		return key
	}
	s := texts[0]
	if lang == "de" {
		s = texts[1]
	}
	for k, a := range args {
		s = strings.Replace(s, fmt.Sprintf("%%%d$@", k+1), a, 1)
	}
	for _, a := range args {
		s = strings.Replace(s, "%@", a, 1)
	}
	return s
}
