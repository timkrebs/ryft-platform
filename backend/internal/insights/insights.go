// Package insights turns recent scored days into the short cards on the
// Insights screen. It is pure: a History in, cards out, no I/O.
//
// A rule is a func(History) *model.Insight that returns nil when it has nothing
// to say. Adding one is a function and a line in Rules, e.g.:
//
//	func lowSpO2(h History) *model.Insight {
//		d, ok := h.Latest(func(d Day) bool { return d.SpO2 != nil })
//		if !ok || *d.SpO2 >= 94 {
//			return nil
//		}
//		return &model.Insight{ID: "spo2-low", Severity: model.SeverityNotice, Metric: "spo2", Day: d.Day,
//			Title: "Low overnight SpO₂", Body: fmt.Sprintf("Your overnight SpO₂ averaged %.0f %%.", *d.SpO2)}
//	}
package insights

import (
	"sort"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Window is how many days of history Evaluate looks at: 30 for the "best in 30
// days" rule; most rules only use the last 14 (h.Last(14)).
const Window = 30

// Day is one calendar day as the rules see it. A day that was never scored has
// only Day set.
type Day struct {
	Day          string
	Calibrating  bool
	Readiness    *int
	Recovery     *int
	Strain       *float64
	SleepPerf    *int
	SleepDebtMin *int
	HRV, HRVBase *float64
	RHR, RHRBase *float64
	SpO2         *float64
	Bedtime      *time.Time // start of the night's sleep, in the user's timezone
	PRs          []PR       // personal records set that day (Train), newest first
	NapMin       *int       // naps that day
	Consistency  *int       // sleep consistency over the 7 nights ending that day
	SleepBase    *int       // the baseline that day's need started from
}

// BehaviorSignal is a behavior logged "yes" for yesterday with its effect on
// the recovery of the morning after (nil while locked).
type BehaviorSignal struct {
	Name     string // English, like every card
	Recovery *model.BehaviorEffect
}

// Baseline is the user's sleep baseline as the baseline-learned card needs it.
type Baseline struct {
	Source    string     // model.Base*
	Min       int        // in use
	LearnedAt *time.Time // in the user's timezone; nil before the first estimate
}

// PR is a personal record as a card names it.
type PR struct {
	Exercise string  // English name
	Kind     string  // model.PR* kind
	Value    float64 // kg or reps
}

// History is a contiguous run of calendar days, oldest first; the last is today.
// "Consecutive nights" therefore means consecutive calendar days.
type History struct {
	Days     []Day
	Baseline *Baseline
	// Yesterday's journal: the behaviors logged as done, with their effects.
	Behaviors []BehaviorSignal
}

// Rule inspects the history and returns a card, or nil.
type Rule func(History) *model.Insight

// Rules run in this order; Evaluate sorts the output by severity.
var Rules = []Rule{
	hrvBelowBaseline,
	rhrElevated,
	sleepDebt,
	strainWithoutRecovery,
	bedtimeConsistency,
	readinessTrendingUp,
	bestHRV30d,
	personalRecord,
	sleepConsistencyLow,
	napCredit,
	baselineLearned,
	behaviorImpact,
}

var severityRank = map[string]int{model.SeverityWarn: 0, model.SeverityNotice: 1, model.SeverityInfo: 2}

// Evaluate runs every rule: warn first, then notice, then info, rule order within.
func Evaluate(h History) []model.Insight {
	out := []model.Insight{}
	for _, rule := range Rules {
		if in := rule(h); in != nil {
			out = append(out, *in)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return severityRank[out[a].Severity] < severityRank[out[b].Severity] })
	return out
}

// NewHistory lays scored days onto the calendar: n days ending at today (local
// midnight), oldest first, with empty Days where nothing was scored. bedtimes
// maps a day to the start of the night that ended on it.
func NewHistory(today time.Time, n int, scores []model.DailyScore, bedtimes map[string]time.Time) History {
	byDay := make(map[string]model.DailyScore, len(scores))
	for _, s := range scores {
		byDay[s.Day] = s
	}
	h := History{Days: make([]Day, n)}
	for k := range n {
		day := today.AddDate(0, 0, k-(n-1)).Format("2006-01-02")
		d := Day{Day: day}
		if s, ok := byDay[day]; ok {
			d = Day{Day: day, Calibrating: s.Calibrating, Readiness: s.Readiness, Recovery: s.Recovery,
				Strain: s.Strain, SleepPerf: s.SleepPerformance, SleepDebtMin: s.SleepDebtMin,
				HRV: s.HRVms, HRVBase: s.HRVBaselineMS, RHR: s.RHR, RHRBase: s.RHRBaseline, SpO2: s.SpO2,
				NapMin: s.NapMin, Consistency: s.SleepConsistency, SleepBase: s.SleepNeedBaseMin}
		}
		if b, ok := bedtimes[day]; ok {
			d.Bedtime = &b
		}
		h.Days[k] = d
	}
	return h
}

// WithPRs puts personal records (by local day) on the history's days.
func (h History) WithPRs(byDay map[string][]PR) History {
	for k := range h.Days {
		h.Days[k].PRs = byDay[h.Days[k].Day]
	}
	return h
}

// WithBehaviors adds yesterday's journal.
func (h History) WithBehaviors(b []BehaviorSignal) History {
	h.Behaviors = b
	return h
}

// WithBaseline adds the user's sleep baseline.
func (h History) WithBaseline(b Baseline) History {
	h.Baseline = &b
	return h
}

// Last returns the most recent n days (fewer if the history is shorter).
func (h History) Last(n int) []Day {
	return h.Days[max(0, len(h.Days)-n):]
}

// freshDays is how far back the "latest" value may be: today, or yesterday when
// today has not synced yet.
const freshDays = 2

// Latest returns the most recent day for which has is true, if that day is
// today or yesterday.
func (h History) Latest(has func(Day) bool) (Day, bool) {
	for _, d := range reverse(h.Last(freshDays)) {
		if has(d) {
			return d, true
		}
	}
	return Day{}, false
}

// Streak counts consecutive days satisfying cond, walking back from the most
// recent day for which has is true (today or yesterday). A day without the
// signal ends the streak. last is the day the streak ends on.
func (h History) Streak(has, cond func(Day) bool) (n int, last Day) {
	days := h.Days
	start := len(days) - 1
	for start >= 0 && start >= len(days)-freshDays && !has(days[start]) {
		start--
	}
	if start < 0 || start < len(days)-freshDays {
		return 0, Day{}
	}
	for k := start; k >= 0 && has(days[k]) && cond(days[k]); k-- {
		n++
	}
	return n, days[start]
}

func reverse(days []Day) []Day {
	out := make([]Day, len(days))
	for k, d := range days {
		out[len(days)-1-k] = d
	}
	return out
}
