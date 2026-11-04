package scoring

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// The golden files' generator (golden_test.go runs it): six synthetic users ×
// 30 days, 2026-10-05 .. 2026-11-03. The window holds both autumn DST changes,
// Europe/Berlin on 2026-10-25 (day 20) and America/New_York on 2026-11-01
// (day 27), and is in the past, so the engine's "never past today" bound never
// cuts in. Times of day are wall-clock times in the user's zone (at), so a
// 25-hour day has data in its last hour.
//
// Deterministic: a PCG stream per user (seeded from the seed and a hash of the
// user's name) drawn only as integers, ids derived by name (uuid v5 of
// "user-a/..."), and the api's checks run against goldenNow, never the clock.

const (
	goldenFirstDay = "2026-10-05" // a Monday
	goldenDays     = 30
	goldenSeed     = 20261104
)

// goldenNow is the "now" of the api's session checks and the max-HR estimate:
// the morning after the last golden day.
var goldenNow = time.Date(2026, 11, 4, 6, 0, 0, 0, time.UTC)

var goldenNS = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://ryftapp.de/scoring/golden"))

// goldenUser is one synthetic user: a readable name (the file name), what
// the month exercises, its settings, and how to generate it.
type goldenUser struct {
	Name      string
	Scenario  string
	TZ        string
	MaxHR     int // 0: not set by hand
	BirthYear int // 0: none; else max HR is the api's estimate for it
	build     func(g *goldenGen)
}

var goldenUsers = []goldenUser{
	{Name: "user-a", TZ: "Europe/Berlin", build: goldenWatchUser,
		Scenario: "Apple Watch and iPhone (whose time in bed loses to the watch's night): runs Tue/Thu/Sat, a ride and a run on day 8, a bad night on day 11, the watch charging overnight on day 16 (the phone's night only), no resting HR on days 16-17, a sedentary DST day 20, a sick day 27 after a long run (depleted)"},
	{Name: "user-b", TZ: "America/New_York", build: goldenGarminUser,
		Scenario: "Garmin only, no HRV, respiration or SpO2: nights without stages, heart rate every 2 min, rides with an average heart rate (Mondays without samples), no data on day 9, the day-16 ride re-sent with a new id (folded), DST on day 27"},
	{Name: "user-c", TZ: "Europe/Berlin", MaxHR: 186, build: goldenTwoSourceUser,
		Scenario: "Apple Watch and Garmin worn together: both write heart rate, steps, resting HR and the night; runs recorded by both (one shown; day 10's Garmin one is 'other'); short watch-only intervals on Tuesdays in an hour the Garmin's heart rate wins; manual max HR 186; DST on day 20"},
	{Name: "user-d", TZ: "UTC", build: goldenNapUser,
		Scenario: "Apple Watch: afternoon naps every third day (one under 20 min, one over the 90-min cap), split nights every fifth day, a morning nap on day 18, a run across midnight from day 13 into day 14"},
	{Name: "user-e", TZ: "UTC", build: goldenStrengthUser,
		Scenario: "Apple Watch and strength: sessions Mon/Wed/Fri (Mon and Fri with a watch strength workout, Wed without), an imported history over the 13 weeks before with a heavy single that leaves the 90-day window mid-month, and day 16's session imported instead of logged"},
	{Name: "user-f", TZ: "Asia/Tokyo", BirthYear: 1984, build: goldenRingUser,
		Scenario: "A ring tracker that never writes a resting heart rate (no_rhr, strain against the 60-bpm fallback): HRV, respiration, SpO2, staged nights, heart rate every 5 min, steps; max HR estimated from birth year 1984"},
}

// goldenGen builds one user's month: HealthKit records as syncs send them, a
// re-sent copy for the fold, and strength sessions as the app or the import
// sends them.
type goldenGen struct {
	user     string
	rng      *rand.PCG
	loc      *time.Location
	lib      map[string]model.Exercise
	samples  []model.Sample
	sleep    []model.SleepSegment
	workouts []model.Workout
	resends  []model.Workout // sent in a sync of their own after the month
	sessions []goldenSession
}

type goldenSession struct {
	Session  model.Session `json:"session"`
	Imported bool          `json:"imported"`
}

type goldenDevice struct{ name, bundle string }

// window is a workout's span, for the heart rate and steps in it.
type window struct{ from, to time.Time }

func goldenMin(n int) time.Duration { return time.Duration(n) * time.Minute }

func newGoldenGen(u goldenUser, lib map[string]model.Exercise) (*goldenGen, error) {
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		return nil, err
	}
	h := fnv.New64a()
	h.Write([]byte(u.Name))
	g := &goldenGen{user: u.Name, rng: rand.NewPCG(goldenSeed, h.Sum64()), loc: loc, lib: lib}
	u.build(g)
	return g, nil
}

// between is a whole number in [lo, hi]. Integers only: math/rand/v2's PCG
// output is fixed by its definition, and a modulo is the same on every CPU.
func (g *goldenGen) between(lo, hi int) int {
	return lo + int(g.rng.Uint64()%uint64(hi-lo+1))
}

// tenths is between(lo, hi)/10: a value with one decimal, as HealthKit shows them.
func (g *goldenGen) tenths(lo, hi int) float64 { return float64(g.between(lo, hi)) / 10 }

// day is local midnight of golden day k; at is h:m on it, wall clock.
func (g *goldenGen) day(k int) time.Time {
	d, _ := time.ParseInLocation(dayLayout, goldenFirstDay, g.loc)
	return d.AddDate(0, 0, k)
}

func (g *goldenGen) at(k, h, m int) time.Time {
	d := g.day(k)
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, g.loc)
}

func (g *goldenGen) id(parts ...string) uuid.UUID {
	return uuid.NewSHA1(goldenNS, []byte(g.user+"/"+strings.Join(parts, "/")))
}

func (g *goldenGen) add(dev goldenDevice, typ, unit string, start, end time.Time, v float64) {
	b := dev.bundle
	g.samples = append(g.samples, model.Sample{Type: typ, Start: start, End: end, Value: v, Unit: unit,
		Source: dev.name, SourceBundleID: &b})
}

// heart is a heart-rate sample every `every` in [from, to), lo..hi bpm.
func (g *goldenGen) heart(dev goldenDevice, from, to time.Time, every time.Duration, lo, hi int) {
	for t := from; t.Before(to); t = t.Add(every) {
		g.add(dev, model.SampleHeartRate, "count/min", t, t, float64(g.between(lo, hi)))
	}
}

// dayHeart is the waking heart rate from `from` to `to`: every `every` at
// 60-88, every minute at lo..hi inside the workouts (in order, not overlapping).
func (g *goldenGen) dayHeart(dev goldenDevice, from, to time.Time, every time.Duration, lo, hi int, ws ...window) {
	t := from
	for _, w := range ws {
		g.heart(dev, t, w.from, every, 60, 88)
		g.heart(dev, w.from, w.to, time.Minute, lo, hi)
		t = w.to
	}
	g.heart(dev, t, to, every, 62, 90)
}

func inAny(t time.Time, ws []window) bool {
	for _, w := range ws {
		if !t.Before(w.from) && t.Before(w.to) {
			return true
		}
	}
	return false
}

// activity is steps (and active energy, when kcal) in buckets of `every`:
// idle values, a workout's inside the windows.
func (g *goldenGen) activity(dev goldenDevice, from, to time.Time, every time.Duration, idleSteps int, kcal bool, ws ...window) {
	per := int(every / (15 * time.Minute)) // the ranges below are per 15 minutes
	for t := from; t.Before(to); t = t.Add(every) {
		lo, hi, klo, khi := 0, idleSteps, 10, 60 // kcal in tenths
		if inAny(t, ws) {
			lo, hi, klo, khi = 1300, 1700, 1100, 1500
		}
		g.add(dev, model.SampleSteps, "count", t, t.Add(every), float64(per*g.between(lo, hi)))
		if kcal {
			g.add(dev, model.SampleActiveEnergy, "kcal", t, t.Add(every), float64(per*g.between(klo, khi))/10)
		}
	}
}

func (g *goldenGen) segment(dev goldenDevice, from, to time.Time, stage string) {
	b := dev.bundle
	g.sleep = append(g.sleep, model.SleepSegment{Start: from, End: to, Stage: stage, Source: dev.name, SourceBundleID: &b})
}

// stagedNight is a night with stages: in bed from bed to wake, asleep from a
// few minutes later in stretches of core, deep, REM and a little awake.
func (g *goldenGen) stagedNight(dev goldenDevice, bed, wake time.Time) {
	g.segment(dev, bed, wake, model.SleepInBed)
	for t := bed.Add(goldenMin(g.between(5, 20))); t.Before(wake); {
		stage := model.SleepCore
		switch r := g.between(0, 99); {
		case r >= 95:
			stage = model.SleepAwake
		case r >= 70:
			stage = model.SleepREM
		case r >= 50:
			stage = model.SleepDeep
		}
		end := minTime(t.Add(goldenMin(g.between(10, 50))), wake)
		g.segment(dev, t, end, stage)
		t = end
	}
}

// plainNight is a night without stages: "asleep" stretches with short awake ones.
func (g *goldenGen) plainNight(dev goldenDevice, bed, wake time.Time) {
	g.segment(dev, bed, wake, model.SleepInBed)
	for t := bed.Add(goldenMin(g.between(8, 25))); t.Before(wake); {
		end := minTime(t.Add(goldenMin(g.between(40, 120))), wake)
		g.segment(dev, t, end, model.SleepAsleep)
		t = end.Add(goldenMin(g.between(3, 10)))
	}
}

// overnight is the night's HRV (two readings), respiration and SpO2; ranges in tenths.
func (g *goldenGen) overnight(dev goldenDevice, bed time.Time, hrvLo, hrvHi, respLo, respHi int) {
	for _, h := range []time.Duration{2 * time.Hour, 4 * time.Hour} {
		at := bed.Add(h)
		g.add(dev, model.SampleHRVSDNN, "ms", at, at, g.tenths(hrvLo, hrvHi))
	}
	at := bed.Add(3 * time.Hour)
	g.add(dev, model.SampleRespiratoryRate, "count/min", at, at, g.tenths(respLo, respHi))
	g.add(dev, model.SampleSpO2, "%", at, at, g.tenths(950, 985))
}

func (g *goldenGen) restingHR(dev goldenDevice, at time.Time, lo, hi int) {
	g.add(dev, model.SampleRestingHR, "count/min", at, at, g.tenths(lo, hi))
}

func (g *goldenGen) workout(dev goldenDevice, activity string, w window, avgHR *float64) model.Workout {
	x := model.Workout{ID: g.id("workout", dev.name, w.from.UTC().Format(time.RFC3339)),
		Activity: activity, Start: w.from, End: w.to, AvgHR: avgHR, Source: dev.name, SourceBundleID: &dev.bundle}
	g.workouts = append(g.workouts, x)
	return x
}

// resend is the source writing w again under a new id, its end 30 s later: a
// same-source copy the sync's fold removes (D146). The copy's id is chosen
// above the original's, so "the most recently uploaded copy" is the same one
// whether the fold decides by created_at or, on a tie, by id.
func (g *goldenGen) resend(w model.Workout) {
	c := w
	for n := 0; ; n++ {
		c.ID = g.id("workout", w.Source, w.Start.UTC().Format(time.RFC3339), "resent", strconv.Itoa(n))
		if bytes.Compare(c.ID[:], w.ID[:]) > 0 {
			break
		}
	}
	c.End = w.End.Add(30 * time.Second)
	g.resends = append(g.resends, c)
}

// night is the usual night ending on day k's morning: to bed 22:45-23:40,
// up 06:20-07:10.
func (g *goldenGen) night(k int) (bed, wake time.Time) {
	return g.at(k-1, 22, 45).Add(goldenMin(g.between(0, 55))), g.at(k, 6, 20).Add(goldenMin(g.between(0, 50)))
}

// watchNight is an Apple Watch's night: stages, HRV, respiration, SpO2 and
// heart rate every 10 minutes.
func (g *goldenGen) watchNight(watch goldenDevice, bed, wake time.Time) {
	g.stagedNight(watch, bed, wake)
	g.overnight(watch, bed, 480, 640, 138, 156)
	g.heart(watch, bed, wake, 10*time.Minute, 48, 58)
}

// ---- the six users ---------------------------------------------------------------

func goldenWatchUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-a"}
	phone := goldenDevice{"iPhone", "com.apple.health.phone-a"}
	for k := range goldenDays {
		bed, wake := g.night(k)
		switch k {
		case 11: // a bad night: late to bed, the usual alarm
			bed, wake = g.at(k, 1, 40), g.at(k, 6, 5)
		case 27: // sick after the long run: late, restless, up early
			bed, wake = g.at(k, 0, 30), g.at(k, 5, 50)
		}
		// The phone's sleep schedule writes time in bed every night; the watch's
		// measured night wins it, except on day 16 when only the phone has one.
		g.segment(phone, bed.Add(-10*time.Minute), wake.Add(5*time.Minute), model.SleepInBed)
		switch k {
		case 16: // the watch charges overnight
		case 27:
			g.stagedNight(watch, bed, wake)
			g.overnight(watch, bed, 330, 360, 172, 180)
			g.heart(watch, bed, wake, 10*time.Minute, 62, 70)
		default:
			g.watchNight(watch, bed, wake)
		}
		switch k {
		case 16, 17: // no resting HR: day 17 has none for itself or the day before
		case 27:
			g.restingHR(watch, wake.Add(time.Hour), 590, 610)
		default:
			g.restingHR(watch, wake.Add(time.Hour), 495, 545)
		}
		var ws []window
		if k == 8 { // a ride before work, then the run: two workouts that day
			w := window{g.at(k, 7, 30), g.at(k, 7, 30).Add(goldenMin(g.between(25, 35)))}
			g.workout(watch, "cycling", w, nil)
			ws = append(ws, w)
		}
		if wd := k % 7; (wd == 1 || wd == 3 || wd == 5) && k != 20 {
			from := g.at(k, 17, 30).Add(goldenMin(g.between(0, 20)))
			dur := g.between(35, 60)
			if wd == 5 {
				dur = g.between(75, 95)
			}
			w := window{from, from.Add(goldenMin(dur))}
			g.workout(watch, "running", w, nil)
			ws = append(ws, w)
		}
		idle := 160
		if k == 20 {
			idle = 25 // sedentary: the couch all day
		}
		g.dayHeart(watch, wake, g.at(k, 23, 50), 5*time.Minute, 142, 166, ws...)
		g.activity(watch, wake, g.at(k, 23, 30), 15*time.Minute, idle, true, ws...)
		// The phone counts steps too, hourly: fewer samples, so the watch wins each hour.
		g.activity(phone, wake, g.at(k, 23, 0), time.Hour, idle, false, ws...)
	}
}

func goldenGarminUser(g *goldenGen) {
	garmin := goldenDevice{"Garmin Connect", "com.garmin.connect.mobile"}
	for k := range goldenDays {
		if k == 9 {
			continue // the watch stayed at home and nothing synced
		}
		bed, wake := g.night(k)
		g.plainNight(garmin, bed, wake)
		g.heart(garmin, bed, wake, 2*time.Minute, 47, 57)
		g.restingHR(garmin, wake.Add(30*time.Minute), 505, 560)
		end := g.at(k, 23, 58)
		if wd := k % 7; wd == 0 || wd == 2 || wd == 4 {
			from := maxTime(g.at(k, 6, 45), wake.Add(10*time.Minute))
			w := window{from, from.Add(goldenMin(g.between(45, 70)))}
			avg := float64(g.between(128, 150))
			ride := g.workout(garmin, "cycling", w, &avg)
			if k == 16 {
				g.resend(ride)
			}
			if wd == 0 { // Mondays: the ride's heart rate never reached Health, only its average
				g.heart(garmin, wake, w.from, 2*time.Minute, 60, 86)
				g.heart(garmin, w.to, end, 2*time.Minute, 62, 90)
			} else {
				g.dayHeart(garmin, wake, end, 2*time.Minute, 125, 152, w)
			}
		} else {
			g.heart(garmin, wake, end, 2*time.Minute, 60, 86)
		}
		g.activity(garmin, wake, g.at(k, 23, 0), time.Hour, 140, true)
	}
}

func goldenTwoSourceUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-c"}
	garmin := goldenDevice{"Garmin Connect", "com.garmin.connect.mobile"}
	for k := range goldenDays {
		bed, wake := g.night(k)
		// Both trackers record the night, a few minutes apart: one source counts.
		g.stagedNight(watch, bed, wake)
		g.stagedNight(garmin, bed.Add(-goldenMin(g.between(0, 6))), wake.Add(goldenMin(g.between(0, 6))))
		g.overnight(watch, bed, 520, 700, 136, 152)
		g.heart(garmin, bed, wake, 2*time.Minute, 47, 57)
		g.heart(watch, bed, wake, 10*time.Minute, 48, 58)
		g.restingHR(watch, wake.Add(time.Hour), 480, 530)
		g.restingHR(garmin, g.at(k, 23, 50), 490, 540)
		end := g.at(k, 23, 55)
		var ws []window
		if k%7 == 1 { // 15 minutes of intervals at noon, the watch only: in an hour
			// whose heart rate the Garmin (every 2 min) wins over the watch
			w := window{g.at(k, 12, 10), g.at(k, 12, 25)}
			g.workout(watch, "hiit", w, nil)
			ws = append(ws, w)
		}
		var run window
		if wd := k % 7; wd == 0 || wd == 3 || wd == 5 {
			from := g.at(k, 18, 0).Add(goldenMin(g.between(0, 15)))
			run = window{from, from.Add(goldenMin(g.between(40, 65)))}
			ws = append(ws, run)
			// One run, two recordings a minute apart; day 10's Garmin calls it "other".
			g.workout(watch, "running", run, nil)
			activity := "running"
			if k == 10 {
				activity = "other"
			}
			avg := float64(g.between(145, 158))
			g.workout(garmin, activity, window{run.from.Add(-time.Minute), run.to.Add(time.Minute)}, &avg)
		}
		// Garmin every 2 min all day (raised in the run); the watch every 5 min
		// while awake and every minute in its workouts, so each hour has one denser source.
		if run.from.IsZero() {
			g.heart(garmin, wake, end, 2*time.Minute, 60, 88)
		} else {
			g.heart(garmin, wake, run.from, 2*time.Minute, 60, 88)
			g.heart(garmin, run.from, run.to, 2*time.Minute, 138, 160)
			g.heart(garmin, run.to, end, 2*time.Minute, 62, 90)
		}
		g.dayHeart(watch, wake, end, 5*time.Minute, 144, 168, ws...)
		g.activity(watch, wake, g.at(k, 23, 0), 15*time.Minute, 170, true, ws...)
		g.activity(garmin, wake, g.at(k, 23, 0), time.Hour, 170, true, ws...)
	}
}

func goldenNapUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-d"}
	late := false // the night after the run across midnight starts late
	for k := range goldenDays {
		bed, wake := g.night(k)
		if late {
			bed, late = g.at(k, 1, 10), false
		}
		var ws []window
		end := g.at(k, 23, 50)
		switch {
		case k == 13: // a run from 23:20 into day 14
			w := window{g.at(k, 23, 20), g.at(k, 23, 20).Add(goldenMin(80))}
			g.workout(watch, "running", w, nil)
			g.heart(watch, w.from, w.to, time.Minute, 140, 160)
			end, late = w.from, true
		case k%7 == 2 || k%7 == 6:
			from := g.at(k, 18, 30)
			w := window{from, from.Add(goldenMin(g.between(30, 50)))}
			g.workout(watch, "running", w, nil)
			ws = append(ws, w)
		}
		switch {
		case k%5 == 2: // a split night: up for over an hour around 02:00, two times in bed
			mid := bed.Add(3*time.Hour + 20*time.Minute)
			back := mid.Add(goldenMin(g.between(70, 95)))
			g.stagedNight(watch, bed, mid)
			g.stagedNight(watch, back, wake)
			g.overnight(watch, bed, 450, 620, 138, 156)
			g.heart(watch, bed, wake, 10*time.Minute, 48, 60)
		case k == 18: // up at 05:30, back to sleep 10:20-11:35: a morning nap
			wake = g.at(k, 5, 30)
			g.watchNight(watch, bed, wake)
			g.stagedNight(watch, g.at(k, 10, 20), g.at(k, 11, 35))
		default:
			g.watchNight(watch, bed, wake)
		}
		g.restingHR(watch, wake.Add(time.Hour), 500, 555)
		g.dayHeart(watch, wake, end, 5*time.Minute, 140, 162, ws...)
		g.activity(watch, wake, g.at(k, 22, 0), 15*time.Minute, 150, true, ws...)
		switch {
		case k == 5: // 15 minutes: under the 20 that count
			g.segment(watch, g.at(k, 14, 0), g.at(k, 14, 15), model.SleepCore)
		case k == 12: // 100 minutes: the credit stops at 90
			g.segment(watch, g.at(k, 13, 30), g.at(k, 15, 10), model.SleepCore)
		case k%3 == 1:
			from := g.at(k, 14, 0)
			g.segment(watch, from, from.Add(goldenMin(g.between(25, 70))), model.SleepCore)
		}
	}
}

// goldenLift is one exercise of a session: sets × reps at kg (0: bodyweight).
type goldenLift struct {
	slug       string
	sets, reps int
	kg         float64
}

// session is a completed strength session: sets every 2.5 minutes, ended
// after length minutes; ids derived from the user, the key and the position.
func (g *goldenGen) session(key string, at time.Time, length int, bw *float64, imported bool, lifts ...goldenLift) {
	s := model.Session{ID: g.id("session", key), Title: key, StartedAt: at, BodyweightKG: bw, UpdatedAt: at}
	clock := at
	for p, l := range lifts {
		ex, ok := g.lib[l.slug]
		if !ok {
			panic("golden: no built-in exercise " + l.slug)
		}
		se := model.SessionExercise{ID: g.id("session", key, strconv.Itoa(p)), ExerciseID: ex.ID, Position: p}
		for k := range l.sets {
			clock = clock.Add(150 * time.Second)
			done, reps := clock, l.reps
			set := model.Set{ID: g.id("session", key, strconv.Itoa(p), strconv.Itoa(k)), SetIndex: k, Reps: &reps,
				CompletedAt: &done, UpdatedAt: done}
			if l.kg > 0 {
				w := l.kg
				set.WeightKG = &w
			}
			se.Sets = append(se.Sets, set)
		}
		s.Exercises = append(s.Exercises, se)
	}
	end := at.Add(goldenMin(length))
	s.EndedAt = &end
	g.sessions = append(g.sessions, goldenSession{Session: s, Imported: imported})
}

// liftDay is session A (squat, bench, row) or B (deadlift, press, pull-ups)
// of training week w; the weights go up 2.5 kg a week.
func (g *goldenGen) liftDay(key string, at time.Time, a bool, w int, bw *float64, imported bool) {
	inc := 2.5 * float64(w)
	if a {
		g.session(key, at, 62, bw, imported, goldenLift{"back-squat", 5, 5, 100 + inc},
			goldenLift{"bench-press", 5, 5, 75 + inc}, goldenLift{"barbell-row", 5, 5, 70 + inc})
		return
	}
	g.session(key, at, 55, bw, imported, goldenLift{"deadlift", 3, 5, 130 + inc},
		goldenLift{"overhead-press", 5, 5, 50 + inc/2}, goldenLift{"pull-up", 4, 8, 0})
}

func goldenStrengthUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-e"}
	// The imported history, as an export lists it (newest first; the import
	// stores it oldest first, train.ImportOrder): Tuesdays and Thursdays over
	// the 13 weeks before, lighter, and a heavy single squat 75 days before day
	// 0, which leaves the sessions' 90-day e1RM window on day 15.
	g.session("import-single", g.at(-75, 19, 30), 20, nil, true, goldenLift{"back-squat", 1, 1, 140})
	for n := 25; n >= 0; n-- {
		k := -91 + 1 + 7*(n/2) + 2*(n%2)
		g.liftDay("import-"+g.day(k).Format(dayLayout), g.at(k, 18, 0), n%2 == 0, n/2-13, nil, true)
	}

	bw := 81.5
	for k := range goldenDays {
		bed, wake := g.night(k)
		var ws []window
		if wd := k % 7; wd == 0 || wd == 2 || wd == 4 {
			first := &bw
			if k > 0 {
				first = nil // later sessions take the last logged body weight
			}
			at := g.at(k, 18, 0)
			if k == 16 { // logged in the other app, imported afterwards
				g.liftDay("import-"+g.day(k).Format(dayLayout), at.Add(10*time.Minute), (k/2)%2 == 0, k/7, nil, true)
			} else {
				g.liftDay("app-"+g.day(k).Format(dayLayout), at, (k/2)%2 == 0, k/7, first, false)
			}
			if wd != 2 { // Mondays and Fridays the watch records the session too
				w := window{at.Add(-3 * time.Minute), at.Add(64 * time.Minute)}
				g.workout(watch, "strength", w, nil)
				ws = append(ws, w)
			}
		}
		g.stagedNight(watch, bed, wake)
		g.overnight(watch, bed, 500, 660, 138, 156)
		g.heart(watch, bed, wake, 10*time.Minute, 47, 57)
		g.restingHR(watch, wake.Add(time.Hour), 480, 530)
		g.dayHeart(watch, wake, g.at(k, 23, 0), 5*time.Minute, 100, 138, ws...)
		g.activity(watch, wake, g.at(k, 22, 0), 15*time.Minute, 130, true)
	}
}

func goldenRingUser(g *goldenGen) {
	ring := goldenDevice{"Ring", "com.example.ring"}
	for k := range goldenDays {
		bed, wake := g.night(k)
		g.stagedNight(ring, bed, wake)
		g.overnight(ring, bed, 380, 520, 140, 158)
		g.heart(ring, bed, wake, 10*time.Minute, 50, 60)
		g.heart(ring, wake, g.at(k, 23, 0), 5*time.Minute, 62, 96)
		g.activity(ring, wake, g.at(k, 22, 0), 15*time.Minute, 220, false)
	}
}

// ---- inputs ----------------------------------------------------------------------

// goldenInputs is what the generator made for a user: counts, and a digest of
// everything (settings and records) that a value change moves. In the
// manifest, so a diff caused by the generator says so, and make golden-update
// can tell a generator change from a model change.
type goldenInputs struct {
	Samples  int    `json:"samples"`
	Sleep    int    `json:"sleep_segments"`
	Workouts int    `json:"workouts"`
	Sessions int    `json:"sessions"`
	Digest   string `json:"digest"`
}

// inputs digests the canonical inputs: the first 64 bits of their SHA-256,
// in decimal (a hex digest would look like a secret to the repository's scanner).
func (g *goldenGen) inputs(t *testing.T, u goldenUser) goldenInputs {
	t.Helper()
	raw, err := json.Marshal(struct {
		TZ                string
		MaxHR, BirthYear  int
		Samples           []model.Sample
		Sleep             []model.SleepSegment
		Workouts, Resends []model.Workout
		Sessions          []goldenSession
	}{u.TZ, u.MaxHR, u.BirthYear, g.samples, g.sleep, g.workouts, g.resends, g.sessions})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return goldenInputs{Samples: len(g.samples), Sleep: len(g.sleep), Workouts: len(g.workouts) + len(g.resends),
		Sessions: len(g.sessions), Digest: strconv.FormatUint(binary.BigEndian.Uint64(sum[:8]), 10)}
}
