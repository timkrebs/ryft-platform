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
type Exercise struct {
	NetKcal, TSS, TRIMP float64
	HasTSS, HasTRIMP    bool
}

// Load uses the power training-load estimate when present.
func (x Exercise) Load() float64 {
	if x.HasTSS {
		return x.TSS
	}
	return x.TRIMP
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
		a, b := .64, 1.92
		switch p.Sex {
		case "female":
			a, b = .86, 1.67
		case "male":
		default:
			a, b = .75, 1.795
		}
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
