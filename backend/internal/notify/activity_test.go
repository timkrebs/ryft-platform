package notify

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

var actNow = time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)

func fptr(v float64) *float64 { return &v }
func sptr(v string) *string   { return &v }

// aCand is a candidate that passes every rule at actNow: a 45-minute run that ended an hour ago.
func aCand() ActivityCandidate {
	end := actNow.Add(-time.Hour)
	return ActivityCandidate{
		WorkoutRef: store.WorkoutRef{ID: uuid.New(), Source: "Garmin Connect", Activity: "running", Start: end.Add(-45 * time.Minute), End: end},
		Strain:     fptr(12.8), CreatedAt: actNow.Add(-time.Minute),
	}
}

func TestActivityRule(t *testing.T) {
	device := uuid.New()
	// seen: the device row's creation, this long before the workout row was inserted
	seen := func(before time.Duration) func(*ActivityCandidate) {
		return func(c *ActivityCandidate) {
			c.DeviceID, c.DeviceSeen = &device, func() *time.Time { v := c.CreatedAt.Add(-before); return &v }()
		}
	}
	endAt := func(end time.Time) func(*ActivityCandidate) {
		return func(c *ActivityCandidate) { c.End, c.Start = end, end.Add(-45*time.Minute) }
	}
	lasting := func(d time.Duration) func(*ActivityCandidate) {
		return func(c *ActivityCandidate) { c.Start = c.End.Add(-d) }
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ActivityCandidate)
		want   Verdict
	}{
		{"every rule passes", func(*ActivityCandidate) {}, Eligible},
		// 1: RYFT's own, or a Train session's
		{"a ryft-session workout skips", func(c *ActivityCandidate) { c.Source = model.SessionSource }, Skip},
		{"a session attached skips", func(c *ActivityCandidate) { id := uuid.New(); c.SessionID = &id }, Skip},
		{"the app's bundle skips", func(c *ActivityCandidate) { c.SourceBundleID = sptr("dev.timkrebs.ryft") }, Skip},
		{"under it, in any case, skips", func(c *ActivityCandidate) { c.SourceBundleID = sptr("DEV.TIMKREBS.RYFT.watchkitapp") }, Skip},
		{"a bundle that only starts with it passes", func(c *ActivityCandidate) { c.SourceBundleID = sptr("dev.timkrebs.ryftx") }, Eligible},
		{"another app's bundle passes", func(c *ActivityCandidate) { c.SourceBundleID = sptr("com.garmin.connect.mobile") }, Eligible},
		{"no bundle (builds before 2026-10-20) passes", func(c *ActivityCandidate) { c.SourceBundleID = nil }, Eligible},
		// 2: scored
		{"no strain skips", func(c *ActivityCandidate) { c.Strain = nil }, Skip},
		{"strain 0 is eligible", func(c *ActivityCandidate) { c.Strain = fptr(0) }, Eligible},
		// 3: over, and recent
		{"end one second ahead skips", endAt(actNow.Add(time.Second)), Skip},
		{"end now is eligible", endAt(actNow), Eligible},
		{"12 h after the end is eligible", endAt(actNow.Add(-12 * time.Hour)), Eligible},
		{"12 h and a second skips", endAt(actNow.Add(-12*time.Hour - time.Second)), Skip},
		// 4: at least 10 minutes
		{"599 s skips", lasting(599 * time.Second), Skip},
		{"600 s is eligible", lasting(600 * time.Second), Eligible},
		// 5: backfill
		{"stored 59m59s after the device was first seen is backfill", seen(59*time.Minute + 59*time.Second), Backfill},
		{"stored 1 h after passes", seen(time.Hour), Eligible},
		{"stored at the very moment the device was first seen is backfill", seen(0), Backfill},
		{"stored a second before its device row was created passes (a merge, a re-created row)", seen(-time.Second), Eligible},
		{"stored a day before its device row was created passes", seen(-24 * time.Hour), Eligible},
		{"no device row passes", func(c *ActivityCandidate) { c.DeviceID, c.DeviceSeen = &device, nil }, Eligible},
		{"no device id passes", func(c *ActivityCandidate) { c.DeviceID, c.DeviceSeen = nil, nil }, Eligible},
		{"a skip outranks backfill", func(c *ActivityCandidate) { seen(time.Minute)(c); c.Strain = nil }, Skip},
	} {
		c := aCand()
		tc.mutate(&c)
		if got := ActivityRule(c, actNow); got != tc.want {
			t.Errorf("%s: verdict %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestActivityInherited(t *testing.T) {
	c := aCand()
	row := func(mutate func(*store.AlertMark)) []store.AlertMark {
		a := store.AlertMark{WorkoutRef: store.WorkoutRef{ID: uuid.New(), Source: c.Source, Activity: c.Activity, Start: c.Start, End: c.End},
			Day: "2026-10-20", Outcome: store.AlertSent}
		mutate(&a)
		return []store.AlertMark{a}
	}
	shift := func(ds, de time.Duration) func(*store.AlertMark) {
		return func(a *store.AlertMark) { a.Start, a.End = c.Start.Add(ds), c.End.Add(de) }
	}
	member := store.WorkoutRef{ID: uuid.New(), Source: "Apple Watch", Activity: "running", Start: c.Start, End: c.End}
	withMember := c
	withMember.Members = []store.WorkoutRef{member}

	for _, tc := range []struct {
		name   string
		cand   ActivityCandidate
		ledger []store.AlertMark
		want   bool
	}{
		{"an empty ledger", c, nil, false},
		{"the candidate's own id", c, row(func(a *store.AlertMark) { a.ID = c.ID; a.Source = "x" }), true},
		{"a member's id", withMember, row(func(a *store.AlertMark) { a.ID = member.ID; a.Source = "x" }), true},
		{"the same copy a rewrite under a new id", c, row(func(*store.AlertMark) {}), true},
		{"starts and ends 120 s apart", c, row(shift(120*time.Second, -120*time.Second)), true},
		{"a start 121 s apart", c, row(shift(121*time.Second, 0)), false},
		{"an end 121 s apart", c, row(shift(0, 121*time.Second)), false},
		{"another activity", c, row(func(a *store.AlertMark) { a.Activity = "cycling" }), false},
		{"another source", c, row(func(a *store.AlertMark) { a.Source = "Apple Watch" }), false},
		{"a member matched by the fold rule", withMember, row(func(a *store.AlertMark) { a.Source = "Apple Watch" }), true},
		{"a member 121 s off", withMember, row(func(a *store.AlertMark) { a.Source = "Apple Watch"; a.Start = member.Start.Add(121 * time.Second) }), false},
	} {
		if got := Inherited(tc.cand, tc.ledger); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestActivityOutcome(t *testing.T) {
	c := aCand()
	other := func() store.AlertMark {
		return store.AlertMark{WorkoutRef: store.WorkoutRef{ID: uuid.New(), Source: "Other", Activity: "cycling",
			Start: c.Start.Add(-5 * time.Hour), End: c.End.Add(-5 * time.Hour)}, Day: "2026-10-20", Outcome: store.AlertSent}
	}
	twin := store.AlertMark{WorkoutRef: store.WorkoutRef{ID: uuid.New(), Source: c.Source, Activity: c.Activity, Start: c.Start, End: c.End}}
	self := store.AlertMark{WorkoutRef: store.WorkoutRef{ID: c.ID}}

	for _, tc := range []struct {
		name   string
		ledger []store.AlertMark
		sent   int
		pre    string
		want   string
	}{
		{"a free day", []store.AlertMark{other()}, 1, "", store.AlertSending},
		{"decided already", []store.AlertMark{other(), self}, 1, "", store.AlertDuplicate},
		{"decided already, whatever else", []store.AlertMark{self, twin}, 3, store.AlertBackfill, store.AlertDuplicate},
		{"a copy of a decided one", []store.AlertMark{twin}, 0, "", store.AlertInherited},
		{"inherited before backfill, muted and the cap", []store.AlertMark{twin}, 3, store.AlertMuted, store.AlertInherited},
		{"backfill", nil, 0, store.AlertBackfill, store.AlertBackfill},
		{"muted", nil, 0, store.AlertMuted, store.AlertMuted},
		{"quiet", nil, 0, store.AlertQuiet, store.AlertQuiet},
		{"no device", nil, 0, store.AlertNoToken, store.AlertNoToken},
		{"the third", nil, 2, "", store.AlertSending},
		{"the fourth", nil, 3, "", store.AlertCapped},
		{"muted is not capped, so it does not count", nil, 3, store.AlertMuted, store.AlertMuted},
	} {
		if got := ActivityOutcome(c, tc.ledger, tc.sent, tc.pre, ActivityDailyCap); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	// A day of five, with a muted one between: the cap counts the sending, never the muted.
	var ledger []store.AlertMark
	var got []string
	for k, pre := range []string{"", "", store.AlertQuiet, "", "", ""} {
		w := aCand()
		w.Start, w.End = c.Start.Add(time.Duration(k)*time.Hour), c.End.Add(time.Duration(k)*time.Hour)
		w.Source = "S" + string(rune('a'+k)) // six workouts of six sources: no copy of another
		sent := 0
		for _, a := range ledger {
			if a.Outcome == store.AlertSending || a.Outcome == store.AlertSent {
				sent++
			}
		}
		o := ActivityOutcome(w, ledger, sent, pre, ActivityDailyCap)
		got = append(got, o)
		ledger = append(ledger, store.AlertMark{WorkoutRef: w.WorkoutRef, Outcome: o})
	}
	want := []string{"sending", "sending", "quiet", "sending", "capped", "capped"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a day of six: %v, want %v", got, want)
	}
}

// nbspChar is U+00A0 NO-BREAK SPACE, written as a code point: check_bytes bans the raw character.
var nbspChar = string(rune(0xA0))

// docActivityRun is the run of docs/API.md's payload example (Garmin Connect, 16:02 to 16:58:30
// UTC, strain 12.8, account in Europe/Berlin). API.md writes the no-break space JSON-escaped.
const docActivityRun = `{ "aps": { "alert": { "title-loc-key": "ACTIVITY_SCORED_TITLE", "title-loc-args": ["Run"],
                      "loc-key": "ACTIVITY_SCORED_BODY", "loc-args": ["12.8", "56\` + `u00a0min"] },
           "sound": "default", "category": "ACTIVITY_SCORED", "thread-id": "ACTIVITY_SCORED",
           "mutable-content": 1 },
  "ryft": { "category": "ACTIVITY_SCORED", "day": "2026-10-20",
            "url": "https://ryftlab.de/activity/7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12",
            "account": "5f0e8c1a-6c1b-4a4e-9d55-1f1d2f6a3b7c" } }`

func TestActivityMessage(t *testing.T) {
	account := uuid.MustParse("5f0e8c1a-6c1b-4a4e-9d55-1f1d2f6a3b7c")
	id := "7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12"
	start := time.Date(2026, 10, 20, 16, 2, 0, 0, time.UTC)
	end := time.Date(2026, 10, 20, 16, 58, 30, 0, time.UTC)

	m := Activity("2026-10-20", id, "running", 12.8, start, end, "en")
	var doc, got any
	if err := json.Unmarshal([]byte(docActivityRun), &doc); err != nil {
		t.Fatal(err)
	}
	payload := m.Payload(account)
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc, got) {
		t.Errorf("API.md's payload example\n want %v\n  got %s", doc, payload)
	}
	// The wire bytes: sorted keys as encoding/json writes a map, and the no-break space raw.
	wire := `{"aps":{"alert":{"loc-args":["12.8","56` + nbspChar + `min"],"loc-key":"ACTIVITY_SCORED_BODY","title-loc-args":["Run"],` +
		`"title-loc-key":"ACTIVITY_SCORED_TITLE"},"category":"ACTIVITY_SCORED","mutable-content":1,"sound":"default",` +
		`"thread-id":"ACTIVITY_SCORED"},"ryft":{"account":"5f0e8c1a-6c1b-4a4e-9d55-1f1d2f6a3b7c","category":"ACTIVITY_SCORED",` +
		`"day":"2026-10-20","url":"https://ryftlab.de/activity/7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12"}}`
	if string(payload) != wire {
		t.Errorf("wire bytes\n want %s\n  got %s", wire, payload)
	}
	if !m.Expires.Equal(end.Add(12 * time.Hour)) {
		t.Errorf("expires %v, want the end + 12 h", m.Expires)
	}

	// German: "Lauf", the decimal comma, "56 Min." with a no-break space.
	de := Activity("2026-10-20", id, "running", 12.8, start, end, "de")
	if de.TitleArgs[0] != "Lauf" || !reflect.DeepEqual(de.BodyArgs, []string{"12,8", "56" + nbspChar + "Min."}) {
		t.Errorf("german: %v %v", de.TitleArgs, de.BodyArgs)
	}
	if got := Render(de.BodyKey, de.BodyArgs, "de"); got != "Belastung 12,8 · 56"+nbspChar+"Min." {
		t.Errorf("german body %q", got)
	}
	if got := Render(m.TitleKey, m.TitleArgs, "en"); got != "Run scored" {
		t.Errorf("title %q", got)
	}

	// A whole strain has no decimal.
	if w := Activity("d", id, "running", 13, start, end, "en"); w.BodyArgs[0] != "13" {
		t.Errorf("whole strain: %v", w.BodyArgs)
	}

	// other, and an activity added later, have no name: the untitled title, no arguments.
	for _, a := range []string{"other", "kitesurfing", ""} {
		u := Activity("d", id, a, 1, start, end, "en")
		if u.TitleKey != "ACTIVITY_SCORED_TITLE_UNTITLED" || len(u.TitleArgs) != 0 {
			t.Errorf("%q: %s %v", a, u.TitleKey, u.TitleArgs)
		}
		if !strings.Contains(string(u.Payload(account)), `"title-loc-args":[]`) {
			t.Errorf("%q: the arguments are an empty list: %s", a, u.Payload(account))
		}
	}

	// The 20 activities the app sends all have an English and a German name.
	for _, a := range []string{"running", "walking", "hiking", "cycling", "swimming", "rowing", "elliptical", "stairs", "strength",
		"functional_strength", "crossfit", "hiit", "yoga", "core", "soccer", "tennis", "xc_skiing", "skiing", "snowboarding", "cooldown"} {
		en, de := Activity("d", id, a, 1, start, end, "en"), Activity("d", id, a, 1, start, end, "de")
		if en.TitleKey != "ACTIVITY_SCORED_TITLE" || len(en.TitleArgs) != 1 || en.TitleArgs[0] == "" ||
			len(de.TitleArgs) != 1 || de.TitleArgs[0] == "" {
			t.Errorf("%s: en %v de %v", a, en.TitleArgs, de.TitleArgs)
		}
	}
	if len(activityNames) != 20 {
		t.Errorf("%d names, the API's table has 20", len(activityNames))
	}

	// The collapse id of the service: 52 bytes, under APNs' 64.
	if collapse := m.Category + "-" + id; collapse != "ACTIVITY_SCORED-"+id || len(collapse) != 52 {
		t.Errorf("collapse id %q (%d bytes)", collapse, len(collapse))
	}
}

func TestActivityDuration(t *testing.T) {
	nb := nbspChar
	for _, tc := range []struct {
		d        time.Duration
		en, de   string
		describe string
	}{
		{56 * time.Minute, "56" + nb + "min", "56" + nb + "Min.", "under an hour"},
		{56*time.Minute + 59*time.Second, "56" + nb + "min", "56" + nb + "Min.", "rounded down"},
		{60 * time.Minute, "1" + nb + "h", "1" + nb + "Std.", "an hour"},
		{72 * time.Minute, "1" + nb + "h 12" + nb + "min", "1" + nb + "Std. 12" + nb + "Min.", "an hour and minutes"},
		{120 * time.Minute, "2" + nb + "h", "2" + nb + "Std.", "whole hours"},
		{10 * time.Minute, "10" + nb + "min", "10" + nb + "Min.", "the shortest announced"},
	} {
		if got := duration(tc.d, "en"); got != tc.en {
			t.Errorf("%s en: %q, want %q", tc.describe, got, tc.en)
		}
		if got := duration(tc.d, "de"); got != tc.de {
			t.Errorf("%s de: %q, want %q", tc.describe, got, tc.de)
		}
	}
}

// The activity name table and the three keys of docs/API.md are the ones the code ships.
func TestActivityNamesAndKeysMatchTheContract(t *testing.T) {
	b, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Skipf("docs/API.md not readable from here: %v", err)
	}
	cells := func(line string) []string {
		parts := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		for k := range parts {
			parts[k] = strings.Trim(strings.TrimSpace(parts[k]), "`")
		}
		return parts
	}
	names, keys := map[string][2]string{}, map[string][2]string{}
	inNames := false
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "| `activity` | en | de |"):
			inNames = true
		case inNames && strings.HasPrefix(line, "| ---"):
		case inNames && strings.HasPrefix(line, "| `"):
			c := cells(line)
			names[c[0]] = [2]string{c[1], c[2]}
		case inNames:
			inNames = false
		case strings.HasPrefix(line, "| `ACTIVITY_SCORED_"):
			c := cells(line)
			keys[c[0]] = [2]string{c[1], c[2]}
		}
	}
	if len(names) != 20 || !reflect.DeepEqual(names, activityNames) {
		t.Errorf("activity names: the API.md table has %d rows\n doc  %v\n code %v", len(names), names, activityNames)
	}
	for _, k := range []string{"ACTIVITY_SCORED_BODY", "ACTIVITY_SCORED_TITLE", "ACTIVITY_SCORED_TITLE_UNTITLED"} {
		if keys[k] != Catalog[k] || keys[k][0] == "" {
			t.Errorf("%s: API.md %q, Catalog %q", k, keys[k], Catalog[k])
		}
	}
}

func TestEveryPayloadCarriesAccount(t *testing.T) {
	account := uuid.New()
	now := time.Now()
	r := 70
	for name, payload := range map[string][]byte{
		"morning":  Morning("d", 1, &r, model.StateBalanced, berlin, "en").Payload(account),
		"session":  Session("d", "s", "t", 1, 0, now, "en").Payload(account),
		"record":   Record("d", "s", "x", model.PRE1RM, 1, now, "en").Payload(account),
		"stale":    Stale("d", 21, now).Payload(account),
		"activity": Activity("d", "w", "running", 1, now, now.Add(time.Hour), "en").Payload(account),
		"silent":   SilentPayload("2026-10-20", account),
	} {
		var p struct {
			RYFT map[string]string `json:"ryft"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.RYFT["account"] != account.String() || p.RYFT["account"] != strings.ToLower(p.RYFT["account"]) {
			t.Errorf("%s: ryft.account %q, want %s", name, p.RYFT["account"], account)
		}
	}
}
