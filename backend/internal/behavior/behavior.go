// Package behavior is the journal's arithmetic (M5): fixed ids of the built-in
// behaviors, what an entry may hold, and how a behavior changes the morning
// after (docs/SCORING.md → Journal, D74–D77). It is pure: no I/O.
package behavior

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

const (
	WindowDays   = 90   // entry days the impact looks at
	MinDays      = 5    // with and without a behavior, to unlock it
	Resamples    = 1000 // bootstrap resamples per effect
	EditableDays = 14   // a journal day can be saved this many days back
	MaxCustom    = 50   // custom behaviors per user
	MaxCount     = 100  // the largest count an entry may hold
)

// namespace derives the built-in behaviors' fixed ids (like train's for exercises).
var namespace = uuid.MustParse("6b0f4d2e-8a3c-4f1b-9d7e-2c5a8b1e3f40")

// BuiltinID is a built-in behavior's id: UUIDv5 of "behavior:<slug>".
func BuiltinID(slug string) uuid.UUID { return uuid.NewSHA1(namespace, []byte("behavior:"+slug)) }

// Slug derives a custom behavior's slug from its name: lowercase words joined by _.
func Slug(name string) string { return strings.ReplaceAll(train.Slug(name), "-", "_") }

// Editable says whether day can be saved at now (the user's local time): not in
// the future, at most EditableDays back.
func Editable(day string, now time.Time) bool {
	d, err := time.ParseInLocation("2006-01-02", day, now.Location())
	if err != nil {
		return false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return !d.After(today) && !d.Before(today.AddDate(0, 0, -EditableDays))
}

// CheckEntry says what is wrong with an entry for a behavior of kind, or nil.
func CheckEntry(kind string, e model.JournalEntry) error {
	switch kind {
	case model.BehaviorYesNo:
		if e.ValueNum != nil || e.ValueTime != nil {
			return errors.New("a yes/no behavior takes value_bool only")
		}
	case model.BehaviorCount:
		if e.ValueTime != nil {
			return errors.New("a count behavior takes no value_time")
		}
		if e.ValueBool && (e.ValueNum == nil || *e.ValueNum < 1 || *e.ValueNum > MaxCount) {
			return fmt.Errorf("a count behavior that happened needs value_num 1-%d", MaxCount)
		}
		if !e.ValueBool && e.ValueNum != nil && *e.ValueNum != 0 {
			return errors.New("value_num must be empty or 0 when value_bool is false")
		}
	case model.BehaviorTime:
		if e.ValueNum != nil {
			return errors.New("a time behavior takes no value_num")
		}
		if e.ValueBool {
			if e.ValueTime == nil {
				return errors.New("a time behavior that happened needs value_time HH:MM")
			}
			if _, err := time.Parse("15:04", *e.ValueTime); err != nil || len(*e.ValueTime) != 5 {
				return errors.New("value_time must be a local time HH:MM")
			}
		} else if e.ValueTime != nil {
			return errors.New("value_time must be empty when value_bool is false")
		}
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	return nil
}

// Morning is what a scored day says about the night before it.
type Morning struct {
	Recovery  *int
	HRV       *float64
	SleepPerf *int
}

// Answer is one saved entry: on day, the behavior did or did not happen.
type Answer struct {
	Day string
	Yes bool
}

// Impacts is GET /v1/behaviors/impact for one user: for each behavior, its
// answers (entry days in the window) against the morning after (mornings, by
// the morning's day), sorted: unlocked first by the size of the recovery
// effect, then locked, fewest days needed first.
func Impacts(user uuid.UUID, behaviors []model.Behavior, answers map[uuid.UUID][]Answer, mornings map[string]Morning) []model.BehaviorImpact {
	out := make([]model.BehaviorImpact, 0, len(behaviors))
	for _, b := range behaviors {
		out = append(out, impact(user, b, answers[b.ID], mornings))
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Status == model.ImpactUnlocked) != (b.Status == model.ImpactUnlocked) {
			return a.Status == model.ImpactUnlocked
		}
		if a.Status == model.ImpactUnlocked {
			if da, db := math.Abs(a.Recovery.Delta), math.Abs(b.Recovery.Delta); da != db {
				return da > db
			}
		} else if na, nb := a.Needed.Yes+a.Needed.No, b.Needed.Yes+b.Needed.No; na != nb {
			return na < nb
		}
		return a.Name < b.Name
	})
	return out
}

func nextDay(day string) string {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, 1).Format("2006-01-02")
}

func impact(user uuid.UUID, b model.Behavior, answers []Answer, mornings map[string]Morning) model.BehaviorImpact {
	var rec, hrv, sleep [2][]float64 // [0] no, [1] yes
	for _, a := range answers {
		m, ok := mornings[nextDay(a.Day)]
		if !ok {
			continue
		}
		k := 0
		if a.Yes {
			k = 1
		}
		if m.Recovery != nil {
			rec[k] = append(rec[k], float64(*m.Recovery))
		}
		if m.HRV != nil && *m.HRV > 0 {
			hrv[k] = append(hrv[k], *m.HRV)
		}
		if m.SleepPerf != nil {
			sleep[k] = append(sleep[k], float64(*m.SleepPerf))
		}
	}
	out := model.BehaviorImpact{BehaviorID: b.ID, Slug: b.Slug, Name: b.Name, Kind: b.Kind, Status: model.ImpactLocked,
		NYes: len(rec[1]), NNo: len(rec[0]),
		Needed: model.ImpactNeeded{Yes: max(0, MinDays-len(rec[1])), No: max(0, MinDays-len(rec[0]))}}
	if out.Needed.Yes > 0 || out.Needed.No > 0 {
		return out
	}
	out.Status = model.ImpactUnlocked
	out.Recovery = Effect(seed(user, b.ID, "recovery"), rec[1], rec[0], false)
	out.HRVPct = Effect(seed(user, b.ID, "hrv"), hrv[1], hrv[0], true)
	out.SleepPerformance = Effect(seed(user, b.ID, "sleep"), sleep[1], sleep[0], false)
	return out
}

// seed makes a user's numbers repeatable: the same data gives the same interval.
func seed(user, behavior uuid.UUID, metric string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(user[:])
	_, _ = h.Write(behavior[:])
	_, _ = h.Write([]byte(metric))
	return h.Sum64()
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// Effect is mean(yes) − mean(no) (relative: in % of mean(no)) with a 90 %
// percentile bootstrap interval: both groups resampled with replacement
// Resamples times, the 5th and 95th percentile of the statistic. nil with fewer
// than MinDays in either group.
func Effect(seed uint64, yes, no []float64, relative bool) *model.BehaviorEffect {
	if len(yes) < MinDays || len(no) < MinDays {
		return nil
	}
	stat := func(y, n float64) float64 {
		if relative {
			if n == 0 {
				return 0
			}
			return (y - n) / n * 100
		}
		return y - n
	}
	r := xorshift(seed | 1)
	boots := make([]float64, Resamples)
	ny, nn := uint64(len(yes)), uint64(len(no))
	for k := range boots {
		var sy, sn float64
		for range yes {
			sy += yes[r.below(ny)]
		}
		for range no {
			sn += no[r.below(nn)]
		}
		boots[k] = stat(sy/float64(len(yes)), sn/float64(len(no)))
	}
	slices.Sort(boots)
	lo, hi := boots[Resamples*5/100], boots[Resamples*95/100-1]
	return &model.BehaviorEffect{Delta: round1(stat(mean(yes), mean(no))), CILo: round1(lo), CIHi: round1(hi),
		Significant: lo > 0 || hi < 0, NYes: len(yes), NNo: len(no)}
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// xorshift is xorshift64*: a small, fast generator; resampling draws millions
// of indexes per request, and math/rand's bounded draw dominated the time.
type xorshift uint64

func (x *xorshift) next() uint64 {
	*x ^= *x >> 12
	*x ^= *x << 25
	*x ^= *x >> 27
	return uint64(*x) * 2685821657736338717
}

// below is an index in [0, n) by multiply-shift on the high 32 bits (the
// bias for n ≤ 90 is under 1e-7).
func (x *xorshift) below(n uint64) int { return int((x.next() >> 32) * n >> 32) }
