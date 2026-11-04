package scoring

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// computeDay's steps, characterised through what it stores. Written against
// computeDay as one function, before it was split into steps, and kept as the
// proof that the split moved nothing: each case is one step's branch, its
// inputs as stored records, its outputs as the columns they land in. The values
// are what the code computed then, not worked out by hand (the calibration
// tests pin what a number should be); each case says why its branch matters.
// The order of the duplicate and the strength pass is pinned by
// TestEngineAttachesSessionsToTheVisibleWorkout, not here. Opt-in like every
// engine test (RYFT_TEST_POSTGRES_URL).

// stepSeed stores a case's records for one user in the case's zone.
type stepSeed struct {
	t       *testing.T
	st      *store.Store
	user    uuid.UUID
	loc     *time.Location
	workout uuid.UUID // the last workout stored, read back as "workout.*"
}

// at is a local wall-clock time, "2006-01-02 15:04" (not midnight plus a
// duration, which is off by an hour on a DST day).
func (s *stepSeed) at(wall string) time.Time {
	s.t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", wall, s.loc)
	if err != nil {
		s.t.Fatal(err)
	}
	return tm
}

func (s *stepSeed) samples(typ, unit string, value float64, walls ...string) {
	s.t.Helper()
	var xs []model.Sample
	for _, w := range walls {
		xs = append(xs, model.Sample{Type: typ, Start: s.at(w), End: s.at(w), Value: value, Unit: unit, Source: "watch"})
	}
	if _, err := s.st.UpsertSamples(context.Background(), s.user, xs); err != nil {
		s.t.Fatal(err)
	}
}

// heartRate stores one sample a minute in [from, to).
func (s *stepSeed) heartRate(bpm float64, from, to string) {
	s.t.Helper()
	var xs []model.Sample
	for at := s.at(from); at.Before(s.at(to)); at = at.Add(time.Minute) {
		xs = append(xs, model.Sample{Type: model.SampleHeartRate, Start: at, End: at, Value: bpm, Unit: "count/min", Source: "watch"})
	}
	if _, err := s.st.UpsertSamples(context.Background(), s.user, xs); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stepSeed) sleep(from, to string) {
	s.t.Helper()
	seg := model.SleepSegment{Start: s.at(from), End: s.at(to), Stage: model.SleepCore, Source: "watch"}
	if _, err := s.st.UpsertSleep(context.Background(), s.user, []model.SleepSegment{seg}); err != nil {
		s.t.Fatal(err)
	}
}

// nights stores a 23:00–07:00 night and a 09:00 resting HR and 03:00 HRV for
// each day: the history recovery's baselines are drawn from.
func (s *stepSeed) nights(days ...string) {
	s.t.Helper()
	for _, d := range days {
		s.sleep(addDays(d, -1)+" 23:00", d+" 07:00")
		s.samples(model.SampleRestingHR, "count/min", 52, d+" 09:00")
		s.samples(model.SampleHRVSDNN, "ms", 64, d+" 03:00")
	}
}

func (s *stepSeed) workoutAvg(activity string, avgHR float64, from, to string) {
	s.t.Helper()
	w := model.Workout{ID: uuid.New(), Activity: activity, Start: s.at(from), End: s.at(to), AvgHR: &avgHR, Source: "watch"}
	if _, err := s.st.UpsertWorkouts(context.Background(), s.user, []model.Workout{w}); err != nil {
		s.t.Fatal(err)
	}
	s.workout = w.ID
}

// stepCase is one step's branch: records, the days scored in order (the last
// is checked) and the columns of that day, as DailyScore's JSON names them
// ("details.x" one level down, "workout.x" for the last workout stored).
type stepCase struct {
	step, name string
	tz         string // "" is UTC
	days       []string
	seed       func(s *stepSeed)
	want       map[string]any
}

// stepCases: numbers are JSON's (float64), a missing column is nil.
var stepCases = []stepCase{
	// ---- loadNight -----------------------------------------------------------
	{step: "loadNight", name: "the overnight window is the sleep, 1 h before to 2 h after",
		// 21:30 is in the night window but before the sleep's hour: an evening
		// reading is not the night's (SCORING.md → Several sources).
		days: []string{"2026-10-14"},
		seed: func(s *stepSeed) {
			s.sleep("2026-10-13 23:00", "2026-10-14 07:00")
			s.samples(model.SampleHRVSDNN, "ms", 100, "2026-10-13 21:30")
			s.samples(model.SampleHRVSDNN, "ms", 40, "2026-10-14 03:00")
			s.samples(model.SampleHRVSDNN, "ms", 50, "2026-10-14 08:30")
			s.samples(model.SampleRespiratoryRate, "count/min", 20, "2026-10-13 21:30")
			s.samples(model.SampleRespiratoryRate, "count/min", 14, "2026-10-14 03:00")
			s.samples(model.SampleSpO2, "%", 96, "2026-10-14 08:30")
		},
		want: map[string]any{"hrv_ms": 45.0, "resp_rate": 14.0, "spo2": 96.0, "sleep_actual_min": 480.0}},
	{step: "loadNight", name: "without a sleep, the night window",
		days: []string{"2026-10-14"},
		seed: func(s *stepSeed) {
			s.samples(model.SampleHRVSDNN, "ms", 100, "2026-10-13 21:30")
			s.samples(model.SampleHRVSDNN, "ms", 50, "2026-10-14 12:00")
			s.samples(model.SampleHRVSDNN, "ms", 10, "2026-10-14 14:00")
		},
		want: map[string]any{"hrv_ms": 75.0, "sleep_actual_min": nil}},
	{step: "loadNight", name: "resting HR falls back to yesterday's last, a 25-hour yesterday",
		// 2026-10-25 is 25 h in Berlin: its 00:30 is more than 24 h before
		// the 26th's midnight, and still yesterday.
		tz: "Europe/Berlin", days: []string{"2026-10-26"},
		seed: func(s *stepSeed) {
			s.samples(model.SampleRestingHR, "count/min", 48, "2026-10-25 00:30")
		},
		want: map[string]any{"rhr": 48.0}},
	{step: "loadNight", name: "yesterday's nap is credited to last night's need (D66)",
		days: []string{"2026-10-14"},
		seed: func(s *stepSeed) {
			s.sleep("2026-10-13 14:00", "2026-10-13 15:00")
			s.sleep("2026-10-13 23:00", "2026-10-14 07:00")
		},
		want: map[string]any{"sleep_need_nap_min": 60.0, "nap_min": 0.0, "sleep_actual_min": 480.0}},

	// ---- baselines -----------------------------------------------------------
	{step: "baselines", name: "yesterday's strain comes from the stored day before",
		days: []string{"2026-10-13", "2026-10-14"},
		seed: func(s *stepSeed) {
			s.nights("2026-10-13", "2026-10-14")
			s.heartRate(165, "2026-10-13 09:30", "2026-10-13 10:30")
		},
		want: map[string]any{"details.yesterday_strain": 16.5, "sleep_need_strain_min": 50.0,
			"details.readiness_penalty": 0.0, "readiness": 75.0}},
	{step: "baselines", name: "consistency counts the last 7 nights, this one included",
		days: []string{"2026-10-10", "2026-10-11", "2026-10-12", "2026-10-13", "2026-10-14"},
		seed: func(s *stepSeed) {
			s.sleep("2026-10-09 22:30", "2026-10-10 06:30")
			s.sleep("2026-10-10 23:00", "2026-10-11 07:00")
			s.sleep("2026-10-11 23:45", "2026-10-12 07:15")
			s.sleep("2026-10-12 23:00", "2026-10-13 07:00")
			s.sleep("2026-10-13 22:45", "2026-10-14 06:45")
		},
		want: map[string]any{"sleep_consistency": 80.0}},
	{step: "baselines", name: "nights older than the week do not count toward consistency",
		days: []string{"2026-10-06", "2026-10-07", "2026-10-11", "2026-10-12", "2026-10-13", "2026-10-14"},
		seed: func(s *stepSeed) {
			s.sleep("2026-10-05 23:00", "2026-10-06 07:00")
			s.sleep("2026-10-06 23:00", "2026-10-07 07:00")
			s.sleep("2026-10-10 23:00", "2026-10-11 07:00")
			s.sleep("2026-10-11 23:00", "2026-10-12 07:00")
			s.sleep("2026-10-12 23:00", "2026-10-13 07:00")
			s.sleep("2026-10-13 23:00", "2026-10-14 07:00")
		},
		want: map[string]any{"sleep_consistency": nil}},

	// ---- strain --------------------------------------------------------------
	{step: "strain", name: "without a resting HR the day's strain uses the baseline's mean",
		days: []string{"2026-10-10", "2026-10-11", "2026-10-12", "2026-10-13", "2026-10-14"},
		seed: func(s *stepSeed) {
			s.samples(model.SampleRestingHR, "count/min", 45, "2026-10-10 09:00", "2026-10-11 09:00", "2026-10-12 09:00")
			s.heartRate(150, "2026-10-14 09:00", "2026-10-14 10:00")
		},
		want: map[string]any{"rhr": nil, "rhr_baseline": 45.0, "strain": 13.3, "details.trimp": 80.0}},
	{step: "strain", name: "without any resting HR the day's strain uses 60",
		days: []string{"2026-10-14"},
		seed: func(s *stepSeed) {
			s.heartRate(150, "2026-10-14 09:00", "2026-10-14 10:00")
		},
		want: map[string]any{"rhr": nil, "strain": 12.1, "details.trimp": 69.0}},
	{step: "strain", name: "a workout without heart rate takes its load from its average (D9)",
		days: []string{"2026-10-14"},
		seed: func(s *stepSeed) {
			s.workoutAvg("running", 150, "2026-10-14 18:00", "2026-10-14 19:00")
		},
		want: map[string]any{"strain": nil, "details.workouts": 1.0, "workout.strain": 12.3, "workout.trimp": 70.3}},
	{step: "strain", name: "the day ends at the next local midnight, 25 hours later",
		// Steps, energy and heart rate at 23:30 on the 25-hour day are still
		// the day's (D22: calendar days, not 24 h).
		tz: "Europe/Berlin", days: []string{"2026-10-25"},
		seed: func(s *stepSeed) {
			s.samples(model.SampleSteps, "count", 100, "2026-10-25 00:30")
			s.samples(model.SampleSteps, "count", 1000, "2026-10-25 23:30")
			s.samples(model.SampleActiveEnergy, "kcal", 50, "2026-10-25 23:30")
			s.heartRate(120, "2026-10-25 23:10", "2026-10-25 23:50")
		},
		want: map[string]any{"steps": 1100.0, "active_kcal": 50.0, "details.hr_minutes": 39.0}},

	// ---- readiness and the row -------------------------------------------------
	{step: "readiness", name: "without a night, readiness is recovery alone",
		days: []string{"2026-10-12", "2026-10-13", "2026-10-14"},
		seed: func(s *stepSeed) {
			for _, d := range []string{"2026-10-12", "2026-10-13", "2026-10-14"} {
				s.samples(model.SampleRestingHR, "count/min", 52, d+" 09:00")
				s.samples(model.SampleHRVSDNN, "ms", 64, d+" 03:00")
			}
		},
		want: map[string]any{"recovery": 50.0, "readiness": 50.0, "sleep_performance": nil, "state": "calibrating",
			"calibrating": true, "calibrating_reason": "short_history"}},
	{step: "readiness", name: "with a night, readiness weighs in the sleep performance",
		days: []string{"2026-10-12", "2026-10-13", "2026-10-14"},
		seed: func(s *stepSeed) {
			s.nights("2026-10-12", "2026-10-13", "2026-10-14")
		},
		want: map[string]any{"recovery": 63.0, "readiness": 78.0, "sleep_performance": 100.0, "sleep_quality": 80.0,
			"hrv_baseline_ms": 64.0, "baseline_days": 2.0, "details.baseline_days": 2.0}},
}

func TestComputeDayStepsStored(t *testing.T) {
	st, engine, _ := engineStore(t)
	for _, c := range stepCases {
		t.Run(c.step+"/"+c.name, func(t *testing.T) {
			got := stepRun(t, st, engine, c)
			for k, w := range c.want {
				if g, ok := got[k]; !ok || !reflect.DeepEqual(g, w) {
					t.Errorf("%s = %v, want %v", k, g, w)
				}
			}
		})
	}
}

// stepRun scores a case for a new user and returns the last day's row (and its
// workout) as flat JSON fields.
func stepRun(t *testing.T, st *store.Store, engine *Engine, c stepCase) map[string]any {
	t.Helper()
	ctx := context.Background()
	user := uuid.New()
	tz := c.tz
	if tz == "" {
		tz = "UTC"
	}
	if _, err := st.EnsureUser(ctx, store.Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(ctx, user, model.UserPatch{TZ: &tz}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, user, "test", nil); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	s := &stepSeed{t: t, st: st, user: user, loc: loc}
	c.seed(s)
	if err := engine.ComputeDays(ctx, user, c.days); err != nil {
		t.Fatal(err)
	}
	day := c.days[len(c.days)-1]
	rows, err := st.DailyScores(ctx, user, day, day)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%s: %v %v", day, rows, err)
	}
	out := flatJSON(t, "", rows[0])
	if s.workout != uuid.Nil {
		w, err := st.Workout(ctx, user, s.workout)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range flatJSON(t, "workout.", map[string]any{"strain": w.Strain, "trimp": w.TRIMP}) {
			out[k] = v
		}
	}
	if testing.Verbose() {
		t.Logf("%s: %v", day, out)
	}
	return out
}

// flatJSON is v's JSON object with "details" one level down as "details.x".
func flatJSON(t *testing.T, prefix string, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for k, v := range m {
		if d, ok := v.(map[string]any); ok && k == "details" {
			for dk, dv := range d {
				out["details."+dk] = dv
			}
			continue
		}
		out[prefix+k] = v
	}
	return out
}
