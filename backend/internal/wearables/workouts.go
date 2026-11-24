package wearables

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Provider identifies the origin and format of a workout observation.
type Provider string

// Supported origins distinguish native FIT, JSON and HealthKit observations.
const (
	GarminFIT  Provider = "GARMIN_FIT"
	SuuntoFIT  Provider = "SUUNTO_FIT"
	GarminJSON Provider = "GARMIN_JSON"
	SuuntoJSON Provider = "SUUNTO_JSON"
	HealthKit  Provider = "APPLE_HEALTHKIT"
	Manual     Provider = "MANUAL"

	startTolerance     = 5 * time.Minute
	durationTolerance  = .10
	maxWorkoutDuration = 48 * time.Hour
)

// Workout holds provenance and the measurements relevant to coaching.
type Workout struct {
	Provider        Provider           `json:"source_provider"`
	ExternalID      string             `json:"external_id"`
	ParentID        string             `json:"parent_workout_id,omitempty"`
	Source          string             `json:"source,omitempty"`
	Bundle          string             `json:"source_bundle_id,omitempty"`
	UserEntered     bool               `json:"was_user_entered"`
	Start           time.Time          `json:"start_at"`
	End             time.Time          `json:"end_at"`
	OffsetSec       int                `json:"tz_offset_sec"`
	Sport           string             `json:"sport_type"`
	DurationSec     float64            `json:"duration_sec"`
	RawKcal         float64            `json:"active_kcal_raw"`
	WorkKJ          float64            `json:"mechanical_work_kj"`
	DistanceM       float64            `json:"distance_m"`
	AvgHR           float64            `json:"avg_hr"`
	MaxHR           float64            `json:"max_hr"`
	NormalizedPower float64            `json:"normalized_power_w"`
	FTP             float64            `json:"ftp_w"`
	HasRR           bool               `json:"rr_intervals_available"`
	Strain          float64            `json:"strain,omitempty"`
	SuuntoPlus      map[string]float64 `json:"suunto_plus,omitempty"`
}

func finiteIn(n, lo, hi float64) bool { return n >= lo && n <= hi }

// Valid bounds workout data before it enters calculation or storage.
func (w Workout) Valid() bool {
	wall := w.End.Sub(w.Start)
	return w.ExternalID != "" && len(w.ExternalID) <= 192 && len(w.ParentID) <= 192 &&
		(w.ParentID == "" || w.ParentID != w.ExternalID) && len(w.Sport) <= 64 && w.Sport != "" &&
		!w.Start.IsZero() && wall > 0 && wall <= maxWorkoutDuration && abs(w.OffsetSec) <= 18*3600 &&
		finiteIn(w.DurationSec, 0, wall.Seconds()+1) &&
		finiteIn(w.RawKcal, 0, 50000) && finiteIn(w.WorkKJ, 0, 50000) && finiteIn(w.DistanceM, 0, 2000000) &&
		finiteIn(w.AvgHR, 0, 250) && finiteIn(w.MaxHR, 0, 250) && finiteIn(w.NormalizedPower, 0, 2000) && finiteIn(w.FTP, 0, 1000)
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// Seconds uses moving time when recorded and otherwise uses elapsed time.
func (w Workout) Seconds() float64 {
	if w.DurationSec > 0 {
		return w.DurationSec
	}
	return w.End.Sub(w.Start).Seconds()
}

// Sport maps provider labels to the sport classes shared by the calculators.
func Sport(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "cycl"), strings.Contains(s, "bik"):
		return "cycling"
	case strings.Contains(s, "run"), strings.Contains(s, "treadmill"):
		return "running"
	case strings.Contains(s, "row"):
		return "rowing"
	case strings.Contains(s, "swim"):
		return "swimming"
	case strings.Contains(s, "strength"), strings.Contains(s, "weight_training"), strings.Contains(s, "resist"):
		return "strength"
	case strings.Contains(s, "walk"), strings.Contains(s, "hik"):
		return "walking"
	case strings.Contains(s, "hiit"), strings.Contains(s, "interval"):
		return "hiit"
	case strings.Contains(s, "triathlon"), strings.Contains(s, "multisport"):
		return "multisport"
	default:
		return s
	}
}

func native(p Provider) bool {
	return p == GarminFIT || p == GarminJSON || p == SuuntoFIT || p == SuuntoJSON
}
func family(p Provider) string {
	switch p {
	case GarminFIT, GarminJSON:
		return "garmin"
	case SuuntoFIT, SuuntoJSON:
		return "suunto"
	default:
		return string(p)
	}
}

// Priority ranks evidence by its origin and measured cycling work.
func (w Workout) Priority() int {
	switch {
	case w.UserEntered || w.Provider == Manual:
		return 5
	case (w.Provider == GarminFIT || w.Provider == SuuntoFIT) && Sport(w.Sport) == "cycling" && w.WorkKJ > 0:
		return 1
	case native(w.Provider):
		return 2
	case strings.HasPrefix(w.Bundle, "com.apple.") || strings.Contains(strings.ToLower(w.Source), "apple watch"):
		return 3
	default:
		return 4
	}
}

// Slice assigns a proportional share of measurements to the retained interval.
func (w Workout) Slice(start, end time.Time) Workout {
	share := end.Sub(start).Seconds() / w.End.Sub(w.Start).Seconds()
	w.DurationSec, w.RawKcal, w.WorkKJ, w.DistanceM = w.Seconds()*share, w.RawKcal*share, w.WorkKJ*share, w.DistanceM*share
	w.Start, w.End = start, end
	return w
}

// Model adapts a reconciled interval to the existing N2R workout input.
func (w Workout) Model() model.Workout {
	return model.Workout{Activity: Sport(w.Sport), Start: w.Start, End: w.End, Source: w.Source, AvgHR: positive(w.AvgHR),
		MaxHR: positive(w.MaxHR), DistanceM: positive(w.DistanceM), Strain: positive(w.Strain)}
}

func positive(v float64) *float64 {
	if v > 0 {
		return &v
	}
	return nil
}

// Reconcile selects the best copy of a session and splits uncovered portions of
// partly overlapping recordings before energy or load is integrated.
func Reconcile(input []Workout) []Workout {
	ordered := slices.Clone(input)
	slices.SortFunc(ordered, func(a, b Workout) int {
		if n := cmp.Compare(a.Priority(), b.Priority()); n != 0 {
			return n
		}
		aFIT := a.Provider == GarminFIT || a.Provider == SuuntoFIT
		bFIT := b.Provider == GarminFIT || b.Provider == SuuntoFIT
		if aFIT != bFIT {
			if aFIT {
				return -1
			}
			return 1
		}
		if n := cmp.Compare(b.Seconds(), a.Seconds()); n != 0 {
			return n
		}
		if n := a.Start.Compare(b.Start); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Provider, b.Provider); n != 0 {
			return n
		}
		return cmp.Compare(a.ExternalID, b.ExternalID)
	})
	children := map[[2]string]bool{}
	for _, w := range ordered {
		if w.Valid() && native(w.Provider) && w.ParentID != "" {
			children[[2]string{family(w.Provider), w.ParentID}] = true
		}
	}
	var chosen, result []Workout
	for _, w := range ordered {
		if !w.Valid() || native(w.Provider) && children[[2]string{family(w.Provider), w.ExternalID}] {
			continue
		}
		w.Sport = Sport(w.Sport)
		pieces := []Workout{w}
		for _, hi := range chosen {
			sameProviderID := native(hi.Provider) && native(w.Provider) && family(hi.Provider) == family(w.Provider) && hi.ExternalID == w.ExternalID
			if sameProviderID || hi.Sport == w.Sport && hi.Start.Before(w.End) && w.Start.Before(hi.End) &&
				math.Abs(hi.Start.Sub(w.Start).Seconds()) <= startTolerance.Seconds() &&
				math.Abs(hi.Seconds()-w.Seconds()) <= durationTolerance*max(hi.Seconds(), w.Seconds()) {
				pieces = nil
				break
			}
		}
		for _, hi := range result {
			if hi.Sport != w.Sport || len(pieces) == 0 {
				continue
			}
			var uncovered []Workout
			for _, piece := range pieces {
				if !piece.Start.Before(hi.End) || !hi.Start.Before(piece.End) {
					uncovered = append(uncovered, piece)
					continue
				}
				if piece.Start.Before(hi.Start) {
					uncovered = append(uncovered, piece.Slice(piece.Start, hi.Start))
				}
				if hi.End.Before(piece.End) {
					uncovered = append(uncovered, piece.Slice(hi.End, piece.End))
				}
			}
			pieces = uncovered
		}
		if len(pieces) > 0 {
			chosen = append(chosen, w)
			result = append(result, pieces...)
		}
	}
	slices.SortStableFunc(result, func(a, b Workout) int { return a.Start.Compare(b.Start) })
	return result
}
