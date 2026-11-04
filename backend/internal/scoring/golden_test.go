package scoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// Golden files: the safety net for refactors (docs/SCORING.md → Model version).
// Five synthetic users × 30 days go through ingest's store calls and the real
// Engine (ComputeRange, as scripts/rescore_all.sh does), and every stored
// daily_scores row, all columns but user_id and computed_at, must equal
// testdata/golden/<user>.json. Nothing here is worked out by hand: the
// calibration tests pin what a number should be, this pins that it did not
// move. A diff means a stored number moved: either a bug, or a model change,
// which bumps ModelVersion (version.go, changelog.json, SCORING.md) and then
// regenerates with `make golden-update`. The update refuses while ModelVersion
// is the one in testdata/golden/manifest.json, so the files cannot be
// regenerated to make a silent change pass.
//
// Determinism: fixed dates (2026-08-03 .. 2026-09-01, all past, so the
// engine's "never past today" bound never cuts in), a PCG stream with a fixed
// seed per user drawn only as integers (no float or library distribution
// whose output could change between Go versions or CPUs), ids derived by name
// (uuid v5 of "user-a/..."), the session's time zone pinned to UTC for the
// read, and canonical JSON (sorted keys, numbers rounded to 1e-6 and printed
// shortest, which also absorbs last-bit differences between CPUs).
//
// Opt-in like every engine test: RYFT_TEST_POSTGRES_URL. TestGoldenManifest
// needs no database and always runs.

const (
	goldenDir      = "testdata/golden"
	goldenFirstDay = "2026-08-03" // a Monday
	goldenDays     = 30
	goldenSeed     = 20261104
	goldenUpdate   = "RYFT_GOLDEN_UPDATE" // =1: rewrite the files (make golden-update)
	goldenMaxLines = 60                   // changed fields shown per user
)

// goldenVolatile are the daily_scores columns left out: the row's owner (a
// generated id) and the wall clock of the write.
var goldenVolatile = []string{"user_id", "computed_at"}

// goldenNow is the "now" of the api's session checks: the morning after the
// last golden day. Never the wall clock.
var goldenNow = time.Date(2026, 9, 2, 6, 0, 0, 0, time.UTC)

var goldenNS = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://ryftapp.de/scoring/golden"))

// goldenUser is one synthetic user: a readable name (the file name), what
// the month exercises, and how to generate it.
type goldenUser struct {
	Name     string
	Scenario string
	TZ       string
	MaxHR    int // 0: the default
	build    func(g *goldenGen)
}

var goldenUsers = []goldenUser{
	{Name: "user-a", TZ: "Europe/Berlin", build: goldenWatchUser,
		Scenario: "Apple Watch and iPhone: runs on Tue/Thu/Sat (a long one on Saturdays), a bad night on day 11, the watch charging overnight on day 16 (the phone's time in bed only, no HRV), a sedentary day 20"},
	{Name: "user-b", TZ: "America/New_York", build: goldenGarminUser,
		Scenario: "Garmin only, no HRV, respiration or SpO2: sleep without stages, heart rate every 2 min, rides with an average heart rate (Mondays without samples), no data at all on day 9"},
	{Name: "user-c", TZ: "Europe/Berlin", MaxHR: 186, build: goldenTwoSourceUser,
		Scenario: "Apple Watch and Garmin worn together: both write heart rate, steps, resting heart rate and the night; runs recorded by both (one shown); manual max HR 186"},
	{Name: "user-d", TZ: "UTC", build: goldenNapUser,
		Scenario: "Apple Watch: afternoon naps every third day (one under 20 min, one over the 90-min cap), split nights every fifth day, a morning nap after an early wake on day 18"},
	{Name: "user-e", TZ: "UTC", build: goldenStrengthUser,
		Scenario: "Apple Watch and strength: sessions Mon/Wed/Fri (Mon and Fri with a watch strength workout, Wed without), an imported history May-July with a heavy single, and day 16's session imported instead of logged"},
}

// ---- the generator -------------------------------------------------------------

// goldenGen builds one user's month: HealthKit samples, sleep and workouts as
// a sync sends them, and strength sessions as the app or the import sends them.
type goldenGen struct {
	user     string
	rng      *rand.PCG
	loc      *time.Location
	lib      map[string]model.Exercise
	samples  []model.Sample
	sleep    []model.SleepSegment
	workouts []model.Workout
	sessions []goldenSession
}

type goldenSession struct {
	s        model.Session
	imported bool
}

type goldenDevice struct{ name, bundle string }

func goldenMin(n int) time.Duration { return time.Duration(n) * time.Minute }

// between is a whole number in [lo, hi]. Integers only: math/rand/v2's PCG
// output is fixed by its definition, and a modulo is the same on every CPU.
func (g *goldenGen) between(lo, hi int) int {
	return lo + int(g.rng.Uint64()%uint64(hi-lo+1))
}

// tenths is between(lo, hi)/10: a value with one decimal, as HealthKit shows them.
func (g *goldenGen) tenths(lo, hi int) float64 { return float64(g.between(lo, hi)) / 10 }

// day is local midnight of golden day k.
func (g *goldenGen) day(k int) time.Time {
	d, _ := time.ParseInLocation(dayLayout, goldenFirstDay, g.loc)
	return d.AddDate(0, 0, k)
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

// activity is steps (and active energy, when kcal) in buckets of `every` from
// wake to 22:00: idle values, the workout's inside [ws, we).
func (g *goldenGen) activity(dev goldenDevice, from, to time.Time, every time.Duration, idleSteps int, kcal bool, ws, we time.Time) {
	for t := from; t.Before(to); t = t.Add(every) {
		per := every.Minutes() / 15              // the ranges below are per 15 minutes
		lo, hi, klo, khi := 0, idleSteps, 10, 60 // tenths of kcal
		if !ws.IsZero() && !t.Before(ws) && t.Before(we) {
			lo, hi, klo, khi = 1300, 1700, 1100, 1500
		}
		g.add(dev, model.SampleSteps, "count", t, t.Add(every), float64(int(per*float64(g.between(lo, hi)))))
		if kcal {
			g.add(dev, model.SampleActiveEnergy, "kcal", t, t.Add(every), math.Round(per*g.tenths(klo, khi)*10)/10)
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
		end := t.Add(goldenMin(g.between(10, 50)))
		if end.After(wake) {
			end = wake
		}
		g.segment(dev, t, end, stage)
		t = end
	}
}

// plainNight is a night without stages: "asleep" stretches with short awake ones.
func (g *goldenGen) plainNight(dev goldenDevice, bed, wake time.Time) {
	g.segment(dev, bed, wake, model.SleepInBed)
	for t := bed.Add(goldenMin(g.between(8, 25))); t.Before(wake); {
		end := t.Add(goldenMin(g.between(40, 120)))
		if end.After(wake) {
			end = wake
		}
		g.segment(dev, t, end, model.SleepAsleep)
		t = end.Add(goldenMin(g.between(3, 10)))
	}
}

// overnight is the night's HRV (two readings), respiration and SpO2.
func (g *goldenGen) overnight(dev goldenDevice, bed time.Time, hrvLo, hrvHi int) {
	for _, h := range []time.Duration{2 * time.Hour, 4 * time.Hour} {
		at := bed.Add(h)
		g.add(dev, model.SampleHRVSDNN, "ms", at, at, g.tenths(hrvLo, hrvHi))
	}
	at := bed.Add(3 * time.Hour)
	g.add(dev, model.SampleRespiratoryRate, "count/min", at, at, g.tenths(138, 156))
	g.add(dev, model.SampleSpO2, "%", at, at, g.tenths(950, 985))
}

func (g *goldenGen) restingHR(dev goldenDevice, at time.Time, lo, hi int) {
	g.add(dev, model.SampleRestingHR, "count/min", at, at, g.tenths(lo, hi))
}

func (g *goldenGen) workout(dev goldenDevice, activity string, from, to time.Time, avgHR *float64) {
	g.workouts = append(g.workouts, model.Workout{ID: g.id("workout", dev.name, from.UTC().Format(time.RFC3339)),
		Activity: activity, Start: from, End: to, AvgHR: avgHR, Source: dev.name, SourceBundleID: &dev.bundle})
}

// night is the usual night ending on day d's morning: to bed 22:45-23:40,
// up 06:20-07:10.
func (g *goldenGen) night(d time.Time) (bed, wake time.Time) {
	return d.Add(-75*time.Minute + goldenMin(g.between(0, 55))), d.Add(6*time.Hour + 20*time.Minute + goldenMin(g.between(0, 50)))
}

// watchDay is a day of an Apple Watch worn day and night: the night with
// stages and its physiology, a resting heart rate, heart rate every 5 min
// (every minute in the workout window [ws, we)), steps and energy every 15 min.
func (g *goldenGen) watchDay(watch goldenDevice, d, bed, wake, ws, we time.Time, idleSteps int) {
	g.stagedNight(watch, bed, wake)
	g.overnight(watch, bed, 480, 640)
	g.heart(watch, bed, wake, 10*time.Minute, 48, 58)
	g.restingHR(watch, wake.Add(time.Hour), 495, 545)
	g.dayHeart(watch, wake, d.Add(22*time.Hour+30*time.Minute), ws, we, 142, 166)
	g.activity(watch, wake, d.Add(22*time.Hour), 15*time.Minute, idleSteps, true, ws, we)
}

// dayHeart is the waking heart rate: every 5 min at 60-88, every minute in a
// workout window at lo..hi.
func (g *goldenGen) dayHeart(dev goldenDevice, from, to, ws, we time.Time, lo, hi int) {
	if ws.IsZero() {
		g.heart(dev, from, to, 5*time.Minute, 60, 88)
		return
	}
	g.heart(dev, from, ws, 5*time.Minute, 60, 88)
	g.heart(dev, ws, we, time.Minute, lo, hi)
	g.heart(dev, we, to, 5*time.Minute, 64, 92)
}

// ---- the five users ------------------------------------------------------------

func goldenWatchUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-a"}
	phone := goldenDevice{"iPhone", "com.apple.health.phone-a"}
	for k := range goldenDays {
		d := g.day(k)
		bed, wake := g.night(d)
		if k == 11 { // a bad night: late to bed, the usual alarm
			bed, wake = d.Add(time.Hour+40*time.Minute), d.Add(6*time.Hour+5*time.Minute)
		}
		var ws, we time.Time
		if wd := k % 7; (wd == 1 || wd == 3 || wd == 5) && k != 20 {
			ws = d.Add(17*time.Hour + 30*time.Minute + goldenMin(g.between(0, 20)))
			dur := g.between(35, 60)
			if wd == 5 {
				dur = g.between(75, 95)
			}
			we = ws.Add(goldenMin(dur))
			g.workout(watch, "running", ws, we, nil)
		}
		idle := 160
		if k == 20 {
			idle = 25 // sedentary: the couch all day
		}
		if k == 16 { // the watch charges overnight: the phone's time in bed, no HRV, no night heart rate
			g.segment(phone, bed, wake, model.SleepInBed)
			g.restingHR(watch, wake.Add(time.Hour), 495, 545)
			g.dayHeart(watch, wake, d.Add(22*time.Hour+30*time.Minute), ws, we, 142, 166)
			g.activity(watch, wake, d.Add(22*time.Hour), 15*time.Minute, idle, true, ws, we)
		} else {
			g.watchDay(watch, d, bed, wake, ws, we, idle)
		}
		// The phone counts steps too, hourly: fewer samples, so the watch wins each hour.
		g.activity(phone, wake, d.Add(22*time.Hour), time.Hour, idle, false, ws, we)
	}
}

func goldenGarminUser(g *goldenGen) {
	garmin := goldenDevice{"Garmin Connect", "com.garmin.connect.mobile"}
	for k := range goldenDays {
		if k == 9 {
			continue // the watch stayed at home and nothing synced
		}
		d := g.day(k)
		bed, wake := g.night(d)
		g.plainNight(garmin, bed, wake)
		g.heart(garmin, bed, wake, 2*time.Minute, 47, 57)
		g.restingHR(garmin, wake.Add(30*time.Minute), 505, 560)
		var ws, we time.Time
		if wd := k % 7; wd == 0 || wd == 2 || wd == 4 {
			ws = d.Add(6*time.Hour + 45*time.Minute)
			if ws.Before(wake.Add(10 * time.Minute)) {
				ws = wake.Add(10 * time.Minute)
			}
			we = ws.Add(goldenMin(g.between(45, 70)))
			avg := float64(g.between(128, 150))
			g.workout(garmin, "cycling", ws, we, &avg)
		}
		end := d.Add(23 * time.Hour)
		switch {
		case ws.IsZero():
			g.heart(garmin, wake, end, 2*time.Minute, 60, 86)
		case k%7 == 0: // Mondays: the ride's heart rate never reached Health, only its average
			g.heart(garmin, wake, ws, 2*time.Minute, 60, 86)
			g.heart(garmin, we, end, 2*time.Minute, 62, 90)
		default:
			g.heart(garmin, wake, ws, 2*time.Minute, 60, 86)
			g.heart(garmin, ws, we, time.Minute, 125, 152)
			g.heart(garmin, we, end, 2*time.Minute, 62, 90)
		}
		g.activity(garmin, wake, d.Add(22*time.Hour), time.Hour, 140, true, time.Time{}, time.Time{})
	}
}

func goldenTwoSourceUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-c"}
	garmin := goldenDevice{"Garmin Connect", "com.garmin.connect.mobile"}
	for k := range goldenDays {
		d := g.day(k)
		bed, wake := g.night(d)
		// Both trackers record the night, a few minutes apart: one source counts.
		g.stagedNight(watch, bed, wake)
		g.stagedNight(garmin, bed.Add(-goldenMin(g.between(0, 6))), wake.Add(goldenMin(g.between(0, 6))))
		g.overnight(watch, bed, 520, 700)
		g.heart(garmin, bed, wake, 2*time.Minute, 47, 57)
		g.heart(watch, bed, wake, 10*time.Minute, 48, 58)
		g.restingHR(watch, wake.Add(time.Hour), 480, 530)
		g.restingHR(garmin, d.Add(23*time.Hour+50*time.Minute), 490, 540)
		var ws, we time.Time
		if wd := k % 7; wd == 0 || wd == 3 || wd == 5 {
			ws = d.Add(18*time.Hour + goldenMin(g.between(0, 15)))
			we = ws.Add(goldenMin(g.between(40, 65)))
			// One run, two recordings: the watch's and the Garmin's, a minute apart.
			g.workout(watch, "running", ws, we, nil)
			avg := float64(g.between(145, 158))
			g.workout(garmin, "running", ws.Add(-time.Minute), we.Add(time.Minute), &avg)
		}
		end := d.Add(23 * time.Hour)
		// Garmin every 2 min all day; the watch every 5 min while awake and every
		// minute during the run, so each hour has one denser source.
		g.heart(garmin, wake, end, 2*time.Minute, 60, 88)
		g.dayHeart(watch, wake, end, ws, we, 144, 168)
		g.activity(watch, wake, d.Add(22*time.Hour), 15*time.Minute, 170, true, ws, we)
		g.activity(garmin, wake, d.Add(22*time.Hour), time.Hour, 170, true, ws, we)
	}
}

func goldenNapUser(g *goldenGen) {
	watch := goldenDevice{"Apple Watch", "com.apple.health.watch-d"}
	for k := range goldenDays {
		d := g.day(k)
		bed, wake := g.night(d)
		var ws, we time.Time
		if k%7 == 2 || k%7 == 6 {
			ws = d.Add(18*time.Hour + 30*time.Minute)
			we = ws.Add(goldenMin(g.between(30, 50)))
			g.workout(watch, "running", ws, we, nil)
		}
		switch {
		case k%5 == 2: // a split night: up for over an hour around 02:00, two times in bed
			mid := bed.Add(3*time.Hour + 20*time.Minute)
			back := mid.Add(goldenMin(g.between(70, 95)))
			g.stagedNight(watch, bed, mid)
			g.stagedNight(watch, back, wake)
			g.overnight(watch, bed, 450, 620)
			g.heart(watch, bed, wake, 10*time.Minute, 48, 60)
			g.restingHR(watch, wake.Add(time.Hour), 500, 555)
			g.dayHeart(watch, wake, d.Add(22*time.Hour+30*time.Minute), ws, we, 140, 162)
			g.activity(watch, wake, d.Add(22*time.Hour), 15*time.Minute, 150, true, ws, we)
		case k == 18: // up at 05:30, back to sleep 10:20-11:35: a morning nap
			wake = d.Add(5*time.Hour + 30*time.Minute)
			g.watchDay(watch, d, bed, wake, ws, we, 150)
			g.stagedNight(watch, d.Add(10*time.Hour+20*time.Minute), d.Add(11*time.Hour+35*time.Minute))
		default:
			g.watchDay(watch, d, bed, wake, ws, we, 150)
		}
		switch {
		case k == 5: // 15 minutes: under the 20 that count
			g.segment(watch, d.Add(14*time.Hour), d.Add(14*time.Hour+15*time.Minute), model.SleepCore)
		case k == 12: // 100 minutes: the credit stops at 90
			g.segment(watch, d.Add(13*time.Hour+30*time.Minute), d.Add(15*time.Hour+10*time.Minute), model.SleepCore)
		case k%3 == 1:
			from := d.Add(14 * time.Hour)
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
	g.sessions = append(g.sessions, goldenSession{s: s, imported: imported})
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
	// The imported history: Tuesdays and Thursdays from May to July, lighter,
	// and a heavy single squat in June that stays inside August's 90-day window.
	hist, _ := time.ParseInLocation(dayLayout, "2026-05-05", g.loc)
	for n := 0; ; n++ {
		d := hist.AddDate(0, 0, 7*(n/2)+2*(n%2))
		if !d.Before(g.day(0).AddDate(0, 0, -3)) {
			break
		}
		g.liftDay("import-"+d.Format(dayLayout), d.Add(18*time.Hour), n%2 == 0, n/2-12, nil, true)
	}
	g.session("import-2026-06-18-single", time.Date(2026, 6, 18, 19, 30, 0, 0, g.loc), 20, nil, true,
		goldenLift{"back-squat", 1, 1, 135})

	bw := 81.5
	for k := range goldenDays {
		d := g.day(k)
		bed, wake := g.night(d)
		var ws, we time.Time
		wd := k % 7
		lift := wd == 0 || wd == 2 || wd == 4
		at := d.Add(18 * time.Hour)
		if lift {
			first := &bw
			if k > 0 {
				first = nil // later sessions take the last logged body weight
			}
			if k == 16 { // logged in the other app, imported afterwards
				g.liftDay("import-"+d.Format(dayLayout), at.Add(10*time.Minute), (k/2)%2 == 0, k/7, nil, true)
			} else {
				g.liftDay("app-"+d.Format(dayLayout), at, (k/2)%2 == 0, k/7, first, false)
			}
			if wd != 2 { // Mondays and Fridays the watch records the session too
				ws, we = at.Add(-3*time.Minute), at.Add(64*time.Minute)
				g.workout(watch, "strength", ws, we, nil)
			}
		}
		g.stagedNight(watch, bed, wake)
		g.overnight(watch, bed, 500, 660)
		g.heart(watch, bed, wake, 10*time.Minute, 47, 57)
		g.restingHR(watch, wake.Add(time.Hour), 480, 530)
		g.dayHeart(watch, wake, d.Add(22*time.Hour+30*time.Minute), ws, we, 100, 138)
		g.activity(watch, wake, d.Add(22*time.Hour), 15*time.Minute, 130, true, time.Time{}, time.Time{})
	}
}

// ---- running a user through the engine ----------------------------------------

// goldenInputs counts what the generator made for a user: in the manifest, so
// a diff caused by a changed generator says so instead of looking like a model change.
type goldenInputs struct {
	Samples  int `json:"samples"`
	Sleep    int `json:"sleep_segments"`
	Workouts int `json:"workouts"`
	Sessions int `json:"sessions"`
}

type goldenManifest struct {
	ModelVersion string                  `json:"model_version"`
	FirstDay     string                  `json:"first_day"`
	Days         int                     `json:"days"`
	Seed         int                     `json:"seed"`
	Users        []string                `json:"users"`
	Inputs       map[string]goldenInputs `json:"inputs"`
	Excluded     []string                `json:"excluded_columns"`
	Numbers      string                  `json:"numbers"`
	Regenerate   string                  `json:"regenerate"`
}

func goldenGenerate(t *testing.T, u goldenUser, k int, lib map[string]model.Exercise) *goldenGen {
	t.Helper()
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		t.Fatal(err)
	}
	g := &goldenGen{user: u.Name, rng: rand.NewPCG(goldenSeed, uint64(k)), loc: loc, lib: lib}
	u.build(g)
	return g
}

func (g *goldenGen) inputs() goldenInputs {
	return goldenInputs{Samples: len(g.samples), Sleep: len(g.sleep), Workouts: len(g.workouts), Sessions: len(g.sessions)}
}

// goldenDB is a fresh database on RYFT_TEST_POSTGRES_URL, migrated and seeded
// (the built-in exercises), with the import's rules wired as the api wires them.
func goldenDB(t *testing.T, admin string) (*store.Store, *pgxpool.Pool, *Engine) {
	t.Helper()
	ctx := context.Background()
	root, err := db.Connect(ctx, admin, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	if err := db.EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ryft_goldentest_%d", rand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool).WithImportRules(store.ImportRules{SameSession: train.SameSession, Conflict: train.ImportConflict,
		SamePlan: train.SamePlan, PlanConflict: train.PlanConflict})
	return st, pool, NewEngine(st, quiet)
}

// goldenRun stores the user's month the way ingest and the api do (one sync a
// day; sessions checked, saved and completed, or imported), scores the 30
// days oldest first and returns the stored rows, canonical, oldest first.
func goldenRun(t *testing.T, st *store.Store, pool *pgxpool.Pool, engine *Engine, u goldenUser, g *goldenGen) []map[string]any {
	t.Helper()
	ctx := context.Background()
	user := uuid.NewSHA1(goldenNS, []byte(u.Name))
	if _, err := st.EnsureUser(ctx, store.Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	patch := model.UserPatch{TZ: &u.TZ}
	if u.MaxHR > 0 {
		patch.MaxHR = &u.MaxHR
	}
	if _, err := st.UpdateUser(ctx, user, patch, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, user, "golden", nil); err != nil {
		t.Fatal(err)
	}

	// One sync per local day (by start): samples, sleep, workouts, sources.
	byDay := func(at time.Time) string { return at.In(g.loc).Format(dayLayout) }
	type sync struct {
		samples  []model.Sample
		sleep    []model.SleepSegment
		workouts []model.Workout
	}
	syncs := map[string]*sync{}
	of := func(at time.Time) *sync {
		d := byDay(at)
		if syncs[d] == nil {
			syncs[d] = &sync{}
		}
		return syncs[d]
	}
	for _, s := range g.samples {
		of(s.Start).samples = append(of(s.Start).samples, s)
	}
	for _, s := range g.sleep {
		of(s.Start).sleep = append(of(s.Start).sleep, s)
	}
	for _, w := range g.workouts {
		of(w.Start).workouts = append(of(w.Start).workouts, w)
	}
	for _, d := range slices.Sorted(maps.Keys(syncs)) {
		s := syncs[d]
		if _, err := st.UpsertSamples(ctx, user, s.samples); err != nil {
			t.Fatalf("%s samples: %v", d, err)
		}
		if _, err := st.UpsertSleep(ctx, user, s.sleep); err != nil {
			t.Fatalf("%s sleep: %v", d, err)
		}
		if _, err := st.UpsertWorkouts(ctx, user, s.workouts); err != nil {
			t.Fatalf("%s workouts: %v", d, err)
		}
		if err := st.UpsertSources(ctx, user, store.SourcesOf(s.samples, s.sleep, s.workouts)); err != nil {
			t.Fatalf("%s sources: %v", d, err)
		}
	}

	exercises := map[uuid.UUID]model.Exercise{}
	for _, e := range g.lib {
		exercises[e.ID] = e
	}
	window := store.DayWindow{From: "2025-08-01", To: goldenNow.Format(dayLayout)}
	for _, gs := range g.sessions {
		s := gs.s
		if gs.imported {
			s.Status = model.SessionCompleted
			if err := train.CheckImportSession(&s, goldenNow, exercises); err != nil {
				t.Fatalf("import %s: %v", s.Title, err)
			}
			out, err := st.ImportSession(ctx, user, model.ImportSourceAlphaProgression, s, g.loc, window)
			if err != nil || out.Status != store.ImportCreated {
				t.Fatalf("import %s: %+v %v", s.Title, out, err)
			}
			continue
		}
		end := *s.EndedAt
		s.Status = model.SessionInProgress
		if err := train.CheckSession(&s, goldenNow); err != nil {
			t.Fatalf("session %s: %v", s.Title, err)
		}
		if _, err := st.SaveSession(ctx, user, s); err != nil {
			t.Fatalf("session %s: %v", s.Title, err)
		}
		if err := st.CompleteSession(ctx, user, s.ID, store.Completion{EndedAt: end}); err != nil {
			t.Fatalf("complete %s: %v", s.Title, err)
		}
	}

	last := addDays(goldenFirstDay, goldenDays-1)
	if err := engine.ComputeRange(ctx, user, goldenFirstDay, last); err != nil {
		t.Fatal(err)
	}
	return goldenRows(t, pool, user)
}

// goldenRows reads the user's daily_scores rows whole, oldest first, as
// Postgres renders them (UTC timestamps, shortest exact floats).
func goldenRows(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) []map[string]any {
	t.Helper()
	ctx := context.Background()
	var raw []string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, set := range []string{"SET LOCAL TIME ZONE 'UTC'", "SET LOCAL extra_float_digits = 1"} {
			if _, err := tx.Exec(ctx, set); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT (to_jsonb(d) - $2::text[])::text FROM daily_scores d WHERE user_id = $1 ORDER BY day`,
			user, goldenVolatile)
		if err != nil {
			return err
		}
		raw, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, len(raw))
	for k, r := range raw {
		if err := goldenDecode([]byte(r), &out[k]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// ---- canonical JSON --------------------------------------------------------------

func goldenDecode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// goldenNumber is a number as the files hold it: rounded to 1e-6 and printed
// shortest, so 12, 12.5 and 0.333333; never -0.
func goldenNumber(n json.Number) string {
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	f = math.Round(f*1e6) / 1e6
	if f == 0 {
		f = 0
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func goldenString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func goldenScalar(v any) bool {
	switch v.(type) {
	case map[string]any:
		return false
	case []any:
		return false
	}
	return true
}

// goldenWrite renders v canonically: keys sorted, numbers as goldenNumber,
// pretty with one key per line (arrays of scalars on one line) or compact.
func goldenWrite(b *strings.Builder, v any, indent string, pretty bool) {
	sep, open, pad := ",", "", ""
	if pretty {
		sep, open, pad = ",\n", "\n", indent+"  "
	}
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{" + open)
		for k, key := range slices.Sorted(maps.Keys(x)) {
			if k > 0 {
				b.WriteString(sep)
			}
			b.WriteString(pad + goldenString(key) + ":")
			if pretty {
				b.WriteString(" ")
			}
			goldenWrite(b, x[key], pad, pretty)
		}
		if pretty {
			b.WriteString("\n" + indent)
		}
		b.WriteString("}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		inline := !pretty || !slices.ContainsFunc(x, func(e any) bool { return !goldenScalar(e) })
		b.WriteString("[")
		for k, e := range x {
			switch {
			case k > 0 && inline:
				b.WriteString(", ")
			case k > 0:
				b.WriteString(",")
			}
			if !inline {
				b.WriteString("\n" + pad)
			}
			goldenWrite(b, e, pad, pretty)
		}
		if !inline {
			b.WriteString("\n" + indent)
		}
		b.WriteString("]")
	case json.Number:
		b.WriteString(goldenNumber(x))
	case string:
		b.WriteString(goldenString(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case nil:
		b.WriteString("null")
	default:
		panic(fmt.Sprintf("golden: unexpected %T", v))
	}
}

func goldenText(v any) string {
	var b strings.Builder
	goldenWrite(&b, v, "", true)
	return b.String() + "\n"
}

// goldenFlatten maps a row to its leaves: "details.recovery.hrv_z" → "0.41";
// arrays are one leaf (zone_minutes reads as one value).
func goldenFlatten(prefix string, v any, out map[string]string) {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		for k, e := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			goldenFlatten(p, e, out)
		}
		return
	}
	var b strings.Builder
	goldenWrite(&b, v, "", false)
	out[prefix] = b.String()
}

// goldenFile is one user's file.
type goldenFile struct {
	User     string           `json:"user"`
	Scenario string           `json:"scenario"`
	TZ       string           `json:"tz"`
	Days     []map[string]any `json:"days"`
}

func goldenPath(name string) string { return filepath.Join(goldenDir, name+".json") }

func readGoldenFile(name string) (goldenFile, error) {
	var f goldenFile
	raw, err := os.ReadFile(goldenPath(name))
	if err != nil {
		return f, err
	}
	return f, goldenDecode(raw, &f)
}

func writeGoldenJSON(t *testing.T, path string, v any) {
	t.Helper()
	// Through the decoder, so every number is a json.Number and printed as goldenNumber.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := goldenDecode(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(goldenText(generic)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGoldenManifest() (goldenManifest, error) {
	var m goldenManifest
	raw, err := os.ReadFile(filepath.Join(goldenDir, "manifest.json"))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(raw, &m)
}

// ---- the diff ------------------------------------------------------------------

// goldenDiff is the readable difference of one user's rows: per day, per
// changed field, golden -> now; at most goldenMaxLines fields, then a count.
func goldenDiff(want, got []map[string]any) (report string, days int) {
	index := func(rows []map[string]any) map[string]map[string]string {
		out := map[string]map[string]string{}
		for _, r := range rows {
			day, _ := r["day"].(string)
			flat := map[string]string{}
			goldenFlatten("", r, flat)
			out[day] = flat
		}
		return out
	}
	w, g := index(want), index(got)
	all := slices.Sorted(maps.Keys(w))
	for d := range g {
		if _, ok := w[d]; !ok {
			all = append(all, d)
		}
	}
	slices.Sort(all)

	type change struct{ day, field, from, to string }
	var changes []change
	for _, d := range all {
		wr, wok := w[d]
		gr, gok := g[d]
		switch {
		case !gok:
			changes = append(changes, change{d, "(row)", "stored", "missing now"})
			continue
		case !wok:
			changes = append(changes, change{d, "(row)", "not in the golden file", "stored now"})
			continue
		}
		fields := slices.Sorted(maps.Keys(wr))
		for f := range gr {
			if _, ok := wr[f]; !ok {
				fields = append(fields, f)
			}
		}
		slices.Sort(fields)
		for _, f := range fields {
			a, aok := wr[f]
			b, bok := gr[f]
			if !aok {
				a = "(absent)"
			}
			if !bok {
				b = "(absent)"
			}
			if a != b {
				changes = append(changes, change{d, f, a, b})
			}
		}
	}
	if len(changes) == 0 {
		return "", 0
	}
	width := 0
	for _, c := range changes[:min(len(changes), goldenMaxLines)] {
		width = max(width, len(c.field))
	}
	var b strings.Builder
	prev := ""
	seen := map[string]bool{}
	for k, c := range changes {
		seen[c.day] = true
		if k >= goldenMaxLines {
			continue
		}
		day := c.day
		if day == prev {
			day = strings.Repeat(" ", len(day))
		}
		prev = c.day
		fmt.Fprintf(&b, "  %s  %-*s  %s -> %s\n", day, width, c.field, c.from, c.to)
	}
	if n := len(changes) - goldenMaxLines; n > 0 {
		fmt.Fprintf(&b, "  ... and %d more changed fields\n", n)
	}
	return b.String(), len(seen)
}

// ---- the tests -----------------------------------------------------------------

// TestGoldenScores recomputes the five users through the real Engine and diffs
// every stored row against testdata/golden. RYFT_GOLDEN_UPDATE=1 (make
// golden-update) rewrites the files instead, only for a new ModelVersion.
func TestGoldenScores(t *testing.T) {
	update := os.Getenv(goldenUpdate) == "1"
	manifest, merr := readGoldenManifest()
	if update && merr == nil && manifest.ModelVersion == ModelVersion {
		t.Fatalf("refusing to regenerate the golden files: ModelVersion is still %s, the version they were made with. "+
			"A change that moves a stored number bumps ModelVersion first (version.go, changelog.json, docs/SCORING.md); "+
			"a change that should not move one must pass as it is", ModelVersion)
	}
	if update && merr != nil && !errors.Is(merr, fs.ErrNotExist) {
		t.Fatalf("manifest: %v", merr)
	}
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		if update {
			t.Fatal("make golden-update needs RYFT_TEST_POSTGRES_URL: a throwaway Postgres server")
		}
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server to run the golden scores")
	}
	if !update && merr != nil {
		t.Fatalf("manifest: %v (generate the files with make golden-update)", merr)
	}

	st, pool, engine := goldenDB(t, admin)
	lib := library(t)
	inputs := map[string]goldenInputs{}
	for k, u := range goldenUsers {
		t.Run(u.Name, func(t *testing.T) {
			g := goldenGenerate(t, u, k, lib)
			inputs[u.Name] = g.inputs()
			rows := goldenRun(t, st, pool, engine, u, g)
			if len(rows) != goldenDays {
				t.Fatalf("%d stored rows, want %d", len(rows), goldenDays)
			}
			if update {
				writeGoldenJSON(t, goldenPath(u.Name), goldenFile{User: u.Name, Scenario: u.Scenario, TZ: u.TZ, Days: rows})
				return
			}
			want, err := readGoldenFile(u.Name)
			if err != nil {
				t.Fatalf("golden file: %v", err)
			}
			report, days := goldenDiff(want.Days, rows)
			if report == "" {
				return
			}
			var hint []string
			if was, ok := manifest.Inputs[u.Name]; ok && was != g.inputs() {
				hint = append(hint, fmt.Sprintf("the generator changed (inputs %+v, the manifest has %+v): this diff is the generator's, not necessarily the model's", g.inputs(), was))
			}
			if manifest.ModelVersion != ModelVersion {
				hint = append(hint, fmt.Sprintf("ModelVersion is %s, the files are %s: review this diff, then make golden-update", ModelVersion, manifest.ModelVersion))
			} else {
				hint = append(hint, "a stored number moved: a bug, or a model change that needs a ModelVersion bump, a changelog entry, docs/SCORING.md and then make golden-update")
			}
			t.Errorf("%s (%s): %d of %d days differ (golden -> now):\n%s%s", u.Name, u.Scenario, days, goldenDays, report,
				strings.Join(hint, "\n"))
		})
	}
	if update && !t.Failed() {
		names := make([]string, len(goldenUsers))
		for k, u := range goldenUsers {
			names[k] = u.Name
		}
		writeGoldenJSON(t, filepath.Join(goldenDir, "manifest.json"), goldenManifest{
			ModelVersion: ModelVersion, FirstDay: goldenFirstDay, Days: goldenDays, Seed: goldenSeed,
			Users: names, Inputs: inputs, Excluded: goldenVolatile,
			Numbers:    "rounded to 1e-6, printed shortest",
			Regenerate: "make golden-update (refused while model_version equals ModelVersion)",
		})
		t.Logf("golden files regenerated for ModelVersion %s: review the diff (git diff %s) before committing", ModelVersion, goldenDir)
	}
}

// TestGoldenManifest needs no database: the files must belong to the current
// ModelVersion (a bump without make golden-update fails here), list every
// user of the generator, and hold 30 consecutive days each, scored by that version.
func TestGoldenManifest(t *testing.T) {
	if os.Getenv(goldenUpdate) == "1" {
		t.Skip("regenerating")
	}
	m, err := readGoldenManifest()
	if err != nil {
		t.Fatalf("manifest: %v (generate the files with make golden-update)", err)
	}
	if m.ModelVersion != ModelVersion {
		t.Fatalf("ModelVersion is %s, the golden files were made with %s: regenerate them with make golden-update "+
			"(RYFT_TEST_POSTGRES_URL) and commit the reviewed diff with the bump", ModelVersion, m.ModelVersion)
	}
	if m.FirstDay != goldenFirstDay || m.Days != goldenDays || m.Seed != goldenSeed {
		t.Errorf("manifest %s × %d, seed %d; the generator %s × %d, seed %d", m.FirstDay, m.Days, m.Seed,
			goldenFirstDay, goldenDays, goldenSeed)
	}
	var names []string
	for _, u := range goldenUsers {
		names = append(names, u.Name)
	}
	if !slices.Equal(m.Users, names) {
		t.Errorf("manifest users %v, the generator's %v", m.Users, names)
	}
	for _, name := range m.Users {
		f, err := readGoldenFile(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.User != name || len(f.Days) != m.Days {
			t.Errorf("%s: user %q with %d days, want %d", name, f.User, len(f.Days), m.Days)
			continue
		}
		for k, r := range f.Days {
			if day := addDays(m.FirstDay, k); r["day"] != day || r["model_version"] != m.ModelVersion {
				t.Errorf("%s day %d: %v scored by %v, want %s by %s", name, k, r["day"], r["model_version"], day, m.ModelVersion)
			}
		}
	}
}

// TestGoldenDiffReads pins the failure message's shape on a made-up change.
func TestGoldenDiffReads(t *testing.T) {
	row := func(rec, z string) map[string]any {
		return map[string]any{"day": "2026-08-14", "recovery": json.Number(rec),
			"details": map[string]any{"recovery": map[string]any{"hrv_z": json.Number(z)}}, "zone_minutes": []any{json.Number("1"), json.Number("2")}}
	}
	report, days := goldenDiff([]map[string]any{row("64", "0.41")}, []map[string]any{row("61", "0.3800000001")})
	want := "  2026-08-14  details.recovery.hrv_z  0.41 -> 0.38\n" +
		"              recovery                64 -> 61\n"
	if report != want || days != 1 {
		t.Fatalf("diff:\n%q\nwant\n%q (%d days)", report, want, days)
	}
	if r, n := goldenDiff([]map[string]any{row("64", "0.41")}, []map[string]any{row("64", "0.4100000001")}); r != "" || n != 0 {
		t.Fatalf("a difference below 1e-6 is none: %q", r)
	}
	if got := goldenText(map[string]any{"b": json.Number("-0.0000001"), "a": []any{json.Number("1.50"), "x"}}); got != "{\n  \"a\": [1.5, \"x\"],\n  \"b\": 0\n}\n" {
		t.Fatalf("canonical text %q", got)
	}
}
