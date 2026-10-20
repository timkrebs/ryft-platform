package scoring

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/insights"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// sourceFixture is what HealthKit hands over for a combination of devices
// (testdata/sources/*.json, D82), and what scoring must make of it. Every
// expectation is worked out by hand in the file's notes, the strain included
// (constant heart rate per stretch makes it arithmetic), and written down
// there: nothing is snapshotted from the code.
type sourceFixture struct {
	Name  string   `json:"name"`
	Notes []string `json:"notes"`
	TZ    string   `json:"tz"`
	Days  []struct {
		Day    string `json:"day"`
		Repeat int    `json:"repeat"` // the same data on this many days, ending on Day
		Series []struct {
			Type     string  `json:"type"`
			Source   string  `json:"source"`
			BundleID *string `json:"bundle_id"`
			From     string  `json:"from"`
			To       string  `json:"to"`
			EveryS   int     `json:"every_s"`
			Value    float64 `json:"value"`
		} `json:"series"`
		Sleep []struct {
			Source   string  `json:"source"`
			BundleID *string `json:"bundle_id"`
			Stage    string  `json:"stage"`
			From     string  `json:"from"`
			To       string  `json:"to"`
		} `json:"sleep"`
		Workouts []struct {
			Source   string  `json:"source"`
			BundleID *string `json:"bundle_id"`
			Activity string  `json:"activity"`
			From     string  `json:"from"`
			To       string  `json:"to"`
		} `json:"workouts"`
		Expect dayExpect `json:"expect"`
	} `json:"days"`
	Sources []struct {
		Name     string   `json:"name"`
		BundleID *string  `json:"bundle_id"`
		Kinds    []string `json:"kinds"`
	} `json:"sources"`
	NoHRCard bool `json:"no_hr_card"`
}

type dayExpect struct {
	HRHours       map[string]int `json:"hr_hours"`
	RHR           float64        `json:"rhr"`
	HRMin         float64        `json:"hr_min"`
	ZoneMin       [5]float64     `json:"zone_min"`
	Strain        *float64       `json:"strain"` // null: no heart rate, no strain
	Steps         int            `json:"steps"`
	ActiveKcal    int            `json:"active_kcal"`
	SleepSource   string         `json:"sleep_source"` // "": no night
	AsleepMin     int            `json:"asleep_min"`
	HasStages     bool           `json:"has_stages"`
	WorkoutSource string         `json:"workout_source"`
	ReadinessNull bool           `json:"readiness_null"`
	State         string         `json:"state"` // checked by the engine test when set
}

// fixtureDay is one expanded day of a fixture: its data and expectations.
type fixtureDay struct {
	day      time.Time
	samples  []model.Sample
	sleep    []model.SleepSegment
	workouts []model.Workout
	expect   dayExpect
}

// fixtureTime reads "HH:MM" on the given day, "-1dHH:MM" on the day before;
// "24:00" is the next midnight.
func fixtureTime(t *testing.T, day time.Time, s string) time.Time {
	t.Helper()
	if rest, ok := strings.CutPrefix(s, "-1d"); ok {
		day, s = day.AddDate(0, 0, -1), rest
	}
	hh, mm, ok := strings.Cut(s, ":")
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if !ok || err1 != nil || err2 != nil {
		t.Fatalf("bad fixture time %q", s)
	}
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// cumulative sample types span their interval; the others are instants.
var cumulative = map[string]bool{model.SampleSteps: true, model.SampleActiveEnergy: true}

func loadSourceFixtures(t *testing.T) map[string]sourceFixture {
	t.Helper()
	files, err := filepath.Glob("testdata/sources/*.json")
	if err != nil || len(files) < 4 {
		t.Fatalf("fixtures: %v %v", files, err)
	}
	out := map[string]sourceFixture{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var f sourceFixture
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		out[strings.TrimSuffix(filepath.Base(file), ".json")] = f
	}
	return out
}

// expand lays a fixture out day by day, oldest first.
func (f sourceFixture) expand(t *testing.T) []fixtureDay {
	t.Helper()
	loc, err := time.LoadLocation(f.TZ)
	if err != nil {
		t.Fatal(err)
	}
	var out []fixtureDay
	for _, fd := range f.Days {
		last, err := time.ParseInLocation("2006-01-02", fd.Day, loc)
		if err != nil {
			t.Fatal(err)
		}
		for k := max(fd.Repeat, 1) - 1; k >= 0; k-- {
			d := fixtureDay{day: last.AddDate(0, 0, -k), expect: fd.Expect}
			for _, s := range fd.Series {
				from, to := fixtureTime(t, d.day, s.From), fixtureTime(t, d.day, s.To)
				step := time.Duration(s.EveryS) * time.Second
				for at := from; at.Before(to); at = at.Add(step) {
					end := at
					if cumulative[s.Type] {
						end = at.Add(step)
					}
					d.samples = append(d.samples, model.Sample{Type: s.Type, Start: at, End: end, Value: s.Value,
						Unit: "fixture", Source: s.Source, SourceBundleID: s.BundleID})
				}
			}
			for _, s := range fd.Sleep {
				d.sleep = append(d.sleep, model.SleepSegment{Start: fixtureTime(t, d.day, s.From), End: fixtureTime(t, d.day, s.To),
					Stage: s.Stage, Source: s.Source, SourceBundleID: s.BundleID})
			}
			for _, w := range fd.Workouts {
				d.workouts = append(d.workouts, model.Workout{ID: uuid.New(), Activity: w.Activity,
					Start: fixtureTime(t, d.day, w.From), End: fixtureTime(t, d.day, w.To), Source: w.Source, SourceBundleID: w.BundleID})
			}
			out = append(out, d)
		}
	}
	return out
}

func samplesOf(xs []model.Sample, typ string, from, to time.Time) []model.Sample {
	var out []model.Sample
	for _, s := range xs {
		if s.Type == typ && !s.Start.Before(from) && s.Start.Before(to) {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b model.Sample) int { return a.Start.Compare(b.Start) })
	return out
}

func sourcesIn[T any](xs []T, src func(T) string) []string {
	var out []string
	for _, x := range xs {
		if s := src(x); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// TestSourceFixtures runs each fixture through the functions computeDay uses,
// without a database.
func TestSourceFixtures(t *testing.T) {
	for name, f := range loadSourceFixtures(t) {
		t.Run(name, func(t *testing.T) {
			days := f.expand(t)
			var all []model.Sample
			var segs []model.SleepSegment
			for _, d := range days {
				all, segs = append(all, d.samples...), append(segs, d.sleep...)
			}
			for _, d := range days {
				checkFixtureDay(t, d, all, segs)
			}
			checkFixtureSources(t, f, days)
		})
	}
}

func checkFixtureDay(t *testing.T, d fixtureDay, all []model.Sample, segs []model.SleepSegment) {
	t.Helper()
	day, end, e := d.day, d.day.AddDate(0, 0, 1), d.expect
	at := day.Format("2006-01-02") + ": "

	hr := DedupSources(samplesOf(all, model.SampleHeartRate, day, end))
	hours := map[string]int{}
	for src, n := range countHours(hr) {
		hours[src] = n
	}
	if len(hours) != len(e.HRHours) || !func() bool {
		for k, v := range e.HRHours {
			if hours[k] != v {
				return false
			}
		}
		return true
	}() {
		t.Errorf("%sheart-rate hours per source %v, want %v", at, hours, e.HRHours)
	}
	rhr := 60.0
	if xs := samplesOf(all, model.SampleRestingHR, day, end); len(xs) > 0 {
		rhr = xs[len(xs)-1].Value
	}
	if rhr != e.RHR {
		t.Errorf("%sresting heart rate %v, want %v", at, rhr, e.RHR)
	}
	s := Strain(hrPoints(hr), rhr, DefaultMaxHR)
	if math.Abs(s.Minutes-e.HRMin) > 1e-6 || !zonesNear(s.ZoneMinutes, e.ZoneMin) {
		t.Errorf("%s%.2f heart-rate minutes, zones %v; want %v, %v", at, s.Minutes, s.ZoneMinutes, e.HRMin, e.ZoneMin)
	}
	if (e.Strain == nil) != (s.Minutes == 0) || (e.Strain != nil && s.Score != *e.Strain) {
		t.Errorf("%sstrain %.1f over %.0f min, want %v", at, s.Score, s.Minutes, e.Strain)
	}
	if n := DayTotal(samplesOf(all, model.SampleSteps, day, end)); n != e.Steps {
		t.Errorf("%ssteps %d, want %d", at, n, e.Steps)
	}
	if n := DayTotal(samplesOf(all, model.SampleActiveEnergy, day, end)); n != e.ActiveKcal {
		t.Errorf("%sactive kcal %d, want %d", at, n, e.ActiveKcal)
	}

	night, _ := SplitDay(segs, day)
	sleep := SummarizeSleep(night)
	src := sourcesIn(night, func(s model.SleepSegment) string { return s.Source })
	want := []string{e.SleepSource}
	if e.SleepSource == "" {
		want = nil
	}
	if !slices.Equal(src, want) || sleep.AsleepMin != e.AsleepMin || sleep.HasStages != e.HasStages {
		t.Errorf("%snight from %v: %d min asleep, stages %v; want %v, %d, %v", at, src, sleep.AsleepMin, sleep.HasStages,
			want, e.AsleepMin, e.HasStages)
	}

	for _, w := range d.workouts {
		from, to := SampleWindow(w.Start, WorkoutWindowEnd(w))
		in := WorkoutSamples(w, samplesOf(all, model.SampleHeartRate, from, to))
		if got := sourcesIn(in, func(s model.Sample) string { return s.Source }); len(in) < 2 ||
			!slices.Equal(got, []string{e.WorkoutSource}) {
			t.Errorf("%s%s's workout curve: %d samples from %v, want %s", at, w.Source, len(in), got, e.WorkoutSource)
		}
	}
}

func countHours(hr []model.Sample) map[string]int {
	seen := map[string]map[int]bool{}
	for _, s := range hr {
		if seen[s.Source] == nil {
			seen[s.Source] = map[int]bool{}
		}
		seen[s.Source][s.Start.Hour()] = true
	}
	out := map[string]int{}
	for src, hs := range seen {
		out[src] = len(hs)
	}
	return out
}

func zonesNear(a, b [5]float64) bool {
	for k := range a {
		if math.Abs(a[k]-b[k]) > 1e-6 {
			return false
		}
	}
	return true
}

// checkFixtureSources: User.sources as ingest records them, and the card.
func checkFixtureSources(t *testing.T, f sourceFixture, days []fixtureDay) {
	t.Helper()
	var samples []model.Sample
	var segs []model.SleepSegment
	var workouts []model.Workout
	for _, d := range days {
		samples, segs, workouts = append(samples, d.samples...), append(segs, d.sleep...), append(workouts, d.workouts...)
	}
	type shown struct {
		bundle *string
		kinds  []string
	}
	by := map[string]*shown{}
	kinds := map[string]bool{}
	for _, s := range store.SourcesOf(samples, segs, workouts) {
		if by[s.Name] == nil {
			by[s.Name] = &shown{}
		}
		by[s.Name].kinds = append(by[s.Name].kinds, s.Kind)
		by[s.Name].bundle = s.BundleID
		kinds[s.Kind] = true
	}
	if len(by) != len(f.Sources) {
		t.Errorf("%d sources, want %d", len(by), len(f.Sources))
	}
	for _, e := range f.Sources {
		g := by[e.Name]
		if g == nil || !slices.Equal(g.kinds, e.Kinds) || g.bundle == nil || e.BundleID == nil || *g.bundle != *e.BundleID {
			t.Errorf("source %s: %+v, want %v %v", e.Name, g, e.Kinds, e.BundleID)
		}
	}
	last := days[len(days)-1].day
	cards := insights.Evaluate(insights.NewHistory(last, 1, nil, nil).WithSourceKinds(kinds))
	if card := slices.ContainsFunc(cards, func(c model.Insight) bool { return c.ID == "no-heart-rate-source" }); card != f.NoHRCard {
		t.Errorf("no-heart-rate-source card %v, want %v", card, f.NoHRCard)
	}
}

// TestEngineSourceFixtures sends each fixture the way ingest stores a sync and
// lets the engine score it: the stored day must say what the fixture says.
func TestEngineSourceFixtures(t *testing.T) {
	for name, f := range loadSourceFixtures(t) {
		t.Run(name, func(t *testing.T) {
			st, engine, user := engineStore(t)
			ctx := context.Background()
			if _, err := st.UpdateUser(ctx, user, model.UserPatch{TZ: &f.TZ}, nil); err != nil {
				t.Fatal(err)
			}
			days := f.expand(t)
			for _, d := range days { // ingest's sync: samples, sleep, workouts, sources
				if _, err := st.UpsertSamples(ctx, user, d.samples); err != nil {
					t.Fatal(err)
				}
				if _, err := st.UpsertSleep(ctx, user, d.sleep); err != nil {
					t.Fatal(err)
				}
				if _, err := st.UpsertWorkouts(ctx, user, d.workouts); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertSources(ctx, user, store.SourcesOf(d.samples, d.sleep, d.workouts)); err != nil {
					t.Fatal(err)
				}
			}
			for _, d := range days {
				if err := engine.ComputeDay(ctx, user, d.day.Format("2006-01-02")); err != nil {
					t.Fatal(err)
				}
			}
			first, last := days[0].day.Format("2006-01-02"), days[len(days)-1].day.Format("2006-01-02")
			scores, err := st.DailyScores(ctx, user, first, last)
			if err != nil || len(scores) != len(days) {
				t.Fatalf("%d scored days, want %d (%v)", len(scores), len(days), err)
			}
			for k, s := range scores {
				checkEngineDay(t, s, days[k].expect)
			}
			src, err := st.UserSources(ctx, user, days[0].day.AddDate(0, 0, -2))
			if err != nil || len(src) != len(f.Sources) {
				t.Fatalf("sources %+v %v", src, err)
			}
			for _, e := range f.Sources {
				k := slices.IndexFunc(src, func(s model.UserSource) bool { return s.Name == e.Name })
				if k < 0 || !slices.Equal(src[k].Kinds, e.Kinds) || src[k].BundleID == nil || *src[k].BundleID != *e.BundleID {
					t.Errorf("stored source %s: %+v", e.Name, src)
				}
			}
		})
	}
}

func checkEngineDay(t *testing.T, s model.DailyScore, e dayExpect) {
	t.Helper()
	at := s.Day + ": "
	if (s.Strain == nil) != (e.Strain == nil) || (e.Strain != nil && *s.Strain != *e.Strain) {
		t.Errorf("%sstored strain %v, want %v", at, s.Strain, e.Strain)
	}
	if s.Steps == nil || *s.Steps != e.Steps || s.ActiveKcal == nil || *s.ActiveKcal != e.ActiveKcal {
		t.Errorf("%sstored steps %v, kcal %v; want %d, %d", at, s.Steps, s.ActiveKcal, e.Steps, e.ActiveKcal)
	}
	if want := e.AsleepMin; (want == 0) != (s.SleepActualMin == nil) || (want > 0 && *s.SleepActualMin != want) {
		t.Errorf("%sstored sleep %v, want %d", at, s.SleepActualMin, want)
	}
	if e.ReadinessNull && s.Readiness != nil {
		t.Errorf("%sreadiness %d, want none", at, *s.Readiness)
	}
	if e.State != "" && s.State != e.State {
		t.Errorf("%sstate %s, want %s", at, s.State, e.State)
	}
	if s.ModelVersion == nil || *s.ModelVersion != ModelVersion {
		t.Errorf("%smodel_version %v", at, s.ModelVersion)
	}
}
