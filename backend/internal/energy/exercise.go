package energy

import (
	"math"

	"github.com/timkrebs/ryft/internal/wearables"
)

// Compensation and DefaultGME seed the baseline and cycling energy models.
const (
	Compensation     = .72
	DefaultGME       = .239
	joulesPerCalorie = 4.184
	minutesPerDay    = 1440

	strengthLowMET  = 3.5
	strengthHighMET = 5
	highHRReserve   = .55
)

// Physiology supplies the individual measurements required to estimate load and expenditure.
type Physiology struct {
	MassKG, RMR, GME, FTP, RestHR, MaxHR float64
	Sex                                  string
}

// Exercise separates measured-load availability from estimated net expenditure.
// RPETRIMP is the load from the user's session RPE (RatedWorkout, D293), set
// only when neither heart rate (TRIMP) nor power (TSS) gives one.
type Exercise struct {
	NetKcal, TSS, TRIMP, RPETRIMP float64
	HasTSS, HasTRIMP, HasRPE      bool
}

// Load uses the power training-load estimate when present, then heart rate,
// then the session RPE.
func (x Exercise) Load() float64 {
	switch {
	case x.HasTSS:
		return x.TSS
	case x.HasTRIMP:
		return x.TRIMP
	}
	return x.RPETRIMP
}

// banister returns Banister's TRIMP weighting constants a and b for the sex
// (Banister 1991; 0.64/1.92 male, 0.86/1.67 female, the midpoint otherwise).
func banister(sex string) (float64, float64) {
	switch sex {
	case "female":
		return .86, 1.67
	case "male":
		return .64, 1.92
	}
	return .75, 1.795
}

// Session RPE (Foster et al. 2001), 1..10 in halves; the api validates the steps.
const (
	minSessionRPE = 1
	maxSessionRPE = 10
)

func validRPE(rpe float64) bool {
	return !math.IsNaN(rpe) && rpe >= minSessionRPE && rpe <= maxSessionRPE
}

// SessionRPELoad is Foster's session load in arbitrary units: the duration in
// whole minutes (rounded) × the rating, rounded (60 min at 8 → 480; 45.4 min
// at 7 → 45 × 7 = 315). Workout.session_rpe_load; display only. 0 without a
// valid rating or a duration.
func SessionRPELoad(minutes, rpe float64) int {
	if !validRPE(rpe) || !(minutes > 0) {
		return 0
	}
	return int(math.Round(math.Round(minutes) * rpe))
}

// SessionRPETRIMP maps a session RPE onto the TRIMP scale (D293): Banister's
// TRIMP with r = rpe/10 in place of the heart-rate-reserve fraction,
// minutes × r × a × e^(b·r), with the sex constants of the heart-rate path.
// Reading RPE × 10 as the reserve percentage is a RYFT heuristic. 60 min at 8
// → 151 (sex unspecified), 143 male, 157 female; 60 min at 3 → 23.
func SessionRPETRIMP(minutes, rpe float64, sex string) float64 {
	if !validRPE(rpe) || !(minutes > 0) {
		return 0
	}
	a, b := banister(sex)
	r := rpe / 10
	return minutes * r * a * math.Exp(b*r)
}

// RatedWorkout is Workout for a workout the user rated (session_rpe; 0 when
// unrated): when neither heart rate nor power yields a load, the rating does
// (RPETRIMP, HasRPE), so the coaching load (acute, chronic, ACWR, TSB) counts
// it. Measured load wins; net energy is Workout's, the rating does not move it.
// Day strain and WorkoutDetail.trimp (internal/scoring) never read it.
func RatedWorkout(w wearables.Workout, p Physiology, rpe float64) Exercise {
	x := Workout(w, p)
	if x.HasTRIMP || x.HasTSS || !validRPE(rpe) {
		return x
	}
	if mins := w.Seconds() / 60; mins > 0 {
		x.RPETRIMP, x.HasRPE = SessionRPETRIMP(mins, rpe, p.Sex), true
	}
	return x
}

// Reserve returns the observed HR reserve fraction when the inputs can establish it.
func Reserve(w wearables.Workout, p Physiology) (float64, bool) {
	if p.MaxHR <= p.RestHR || p.RestHR < 25 || w.AvgHR <= 0 {
		return 0, false
	}
	return min(1, max(0, (w.AvgHR-p.RestHR)/(p.MaxHR-p.RestHR))), true
}

// Workout estimates net energy from trusted power or a duration and intensity model.
func Workout(w wearables.Workout, p Physiology) Exercise {
	mins := w.Seconds() / 60
	if mins <= 0 {
		return Exercise{}
	}
	reserve, validHR := Reserve(w, p)
	x := Exercise{HasTRIMP: validHR}
	if validHR {
		a, b := banister(p.Sex)
		x.TRIMP = mins * reserve * a * math.Exp(b*reserve)
	}
	ftp := w.FTP
	if ftp <= 0 {
		ftp = p.FTP
	}
	if wearables.Sport(w.Sport) == "cycling" && w.NormalizedPower > 0 && ftp > 0 {
		ratio := w.NormalizedPower / ftp
		x.TSS, x.HasTSS = w.Seconds()/3600*ratio*ratio*100, true
	}
	if p.MassKG <= 0 || p.RMR <= 0 {
		return x
	}
	gross := estimatedMET(w, reserve, validHR) * p.MassKG * mins / 60
	if wearables.Sport(w.Sport) == "cycling" && w.WorkKJ > 0 && w.Priority() == 1 {
		gme := p.GME
		if gme < .20 || gme > .25 {
			gme = DefaultGME
		}
		gross = w.WorkKJ / (joulesPerCalorie * gme)
	}
	x.NetKcal = max(0, gross-p.RMR/minutesPerDay*mins)
	return x
}

func estimatedMET(w wearables.Workout, reserve float64, hasHR bool) float64 {
	switch wearables.Sport(w.Sport) {
	case "strength":
		if hasHR && reserve >= highHRReserve {
			return strengthHighMET
		}
		return strengthLowMET
	case "running":
		speedKMH := w.DistanceM / 1000 / (w.Seconds() / 3600)
		if speedKMH > 0 {
			return min(23, max(3, 1+speedKMH/1.05))
		}
		return intensityMET(reserve, hasHR, 7, 6, 8)
	case "cycling":
		return intensityMET(reserve, hasHR, 6, 4, 8)
	case "rowing", "swimming":
		return intensityMET(reserve, hasHR, 6, 4, 7)
	case "walking":
		return intensityMET(reserve, hasHR, 3.5, 2.5, 3)
	case "hiit":
		return intensityMET(reserve, hasHR, 7, 5, 8)
	default:
		return intensityMET(reserve, hasHR, 4, 3, 5)
	}
}

func intensityMET(reserve float64, known bool, fallback, base, span float64) float64 {
	if !known {
		return fallback
	}
	return base + reserve*span
}

// MifflinStJeor estimates resting energy for enrolled adults from supplied measurements.
func MifflinStJeor(mass, height float64, age int, sex string) float64 {
	if mass <= 0 || height <= 0 || age < 18 {
		return 0
	}
	offset := -78.0
	switch sex {
	case "male":
		offset = 5
	case "female":
		offset = -161
	}
	return max(0, 10*mass+6.25*height-5*float64(age)+offset)
}
