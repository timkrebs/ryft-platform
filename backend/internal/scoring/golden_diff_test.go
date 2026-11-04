package scoring

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The golden files' canonical JSON, diff, report and update guard: everything
// that needs no database (golden_test.go runs the users, golden_gen_test.go
// generates them). TestGoldenManifest and TestGoldenDiffReads always run.

const (
	goldenDir      = "testdata/golden"
	goldenUpdate   = "RYFT_GOLDEN_UPDATE" // 1: regenerate (make golden-update); additive: new fields and rows only; init: the first set
	goldenMaxLines = 60                   // report lines per user
)

// The tables pinned, in report order, and the columns left out of each: the
// row's owner (a generated id) and wall-clock stamps of the write.
var goldenTables = []struct {
	section, table, key, order string
	volatile                   []string
}{
	{"days", "daily_scores", "day", "day", []string{"user_id", "computed_at"}},
	{"workouts", "workouts", "id", "start_at, source, id", []string{"user_id", "created_at"}},
	{"sessions", "sessions", "id", "started_at, id", []string{"user_id", "created_at", "updated_at", "imported_at"}},
}

// goldenIDColumns hold ids; the files have labels in their place.
var goldenIDColumns = []string{"id", "session_id", "duplicate_of", "workout_id", "plan_day_id", "device_id"}

// goldenFile is one user's file: the rows of each table, oldest first.
type goldenFile struct {
	User     string           `json:"user"`
	Scenario string           `json:"scenario"`
	TZ       string           `json:"tz"`
	Days     []map[string]any `json:"days"`
	Workouts []map[string]any `json:"workouts"`
	Sessions []map[string]any `json:"sessions"`
}

func (f *goldenFile) section(name string) *[]map[string]any {
	switch name {
	case "days":
		return &f.Days
	case "workouts":
		return &f.Workouts
	}
	return &f.Sessions
}

type goldenManifest struct {
	ModelVersion string                  `json:"model_version"`
	FirstDay     string                  `json:"first_day"`
	Days         int                     `json:"days"`
	Seed         int                     `json:"seed"`
	Users        []string                `json:"users"`
	Inputs       map[string]goldenInputs `json:"inputs"`
	Fields       string                  `json:"fields"` // goldenFields: changes when a column or details key appears or goes
	Excluded     map[string][]string     `json:"excluded_columns"`
	Numbers      string                  `json:"numbers"`
	Regenerate   string                  `json:"regenerate"`
}

// goldenDigest is the first 64 bits of a SHA-256, in decimal (a hex digest
// would look like a secret to the repository's scanner).
func goldenDigest(sum [sha256.Size]byte) string {
	return strconv.FormatUint(binary.BigEndian.Uint64(sum[:8]), 10)
}

// goldenFields digests the field names the files hold (every leaf path of
// every row, per section), not their values: scripts/golden_guard.sh lets a
// user's file change without a ModelVersion bump or a generator change only
// when this did (GOLDEN_MODE=additive, a new column).
func goldenFields(files map[string]goldenFile) string {
	names := map[string]bool{}
	for _, f := range files {
		for _, tb := range goldenTables {
			for _, r := range *f.section(tb.section) {
				flat := map[string]string{}
				goldenFlatten("", r, flat)
				for k := range flat {
					names[tb.section+"."+k] = true
				}
			}
		}
	}
	return goldenDigest(sha256.Sum256([]byte(strings.Join(slices.Sorted(maps.Keys(names)), "\n"))))
}

// ---- canonical JSON ----------------------------------------------------------------

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
	case map[string]any, []any:
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

func goldenDirHasFiles() bool {
	files, _ := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	return len(files) > 0
}

// ---- the diff --------------------------------------------------------------------

// goldenChange is one leaf that differs: changed, added (from is absent) or
// removed (to is absent). A whole row added or removed has field "(row)".
type goldenChange struct {
	section, row, field, from, to string
}

const goldenAbsent = "(absent)"

func (c goldenChange) addition() bool { return c.from == goldenAbsent }

// goldenChanges compares two files leaf by leaf, rows matched by key (the day,
// the label). skipVersion drops the model_version leaves that moved to
// ModelVersion: after a bump every row has one, and the report says so once.
func goldenChanges(want, got goldenFile, skipVersion bool) []goldenChange {
	var out []goldenChange
	for _, tb := range goldenTables {
		index := func(rows []map[string]any) map[string]map[string]string {
			m := map[string]map[string]string{}
			for _, r := range rows {
				key, _ := r[tb.key].(string)
				flat := map[string]string{}
				goldenFlatten("", r, flat)
				m[key] = flat
			}
			return m
		}
		w, g := index(*want.section(tb.section)), index(*got.section(tb.section))
		keys := slices.Sorted(maps.Keys(w))
		for k := range g {
			if _, ok := w[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			wr, wok := w[key]
			gr, gok := g[key]
			switch {
			case !gok:
				out = append(out, goldenChange{tb.section, key, "(row)", "stored", goldenAbsent})
				continue
			case !wok:
				out = append(out, goldenChange{tb.section, key, "(row)", goldenAbsent, "stored"})
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
					a = goldenAbsent
				}
				if !bok {
					b = goldenAbsent
				}
				if a == b || (skipVersion && f == "model_version" && b == goldenString(ModelVersion)) {
					continue
				}
				out = append(out, goldenChange{tb.section, key, f, a, b})
			}
		}
	}
	return out
}

// goldenReport reads the changes grouped by field: a field that moved the
// same way on several rows is one line, otherwise one line per row; at most
// goldenMaxLines lines, then a count.
func goldenReport(changes []goldenChange) string {
	type group struct {
		name string
		cs   []goldenChange
	}
	var groups []*group
	byName := map[string]*group{}
	for _, c := range changes {
		name := c.section + "." + c.field
		if byName[name] == nil {
			byName[name] = &group{name: name}
			groups = append(groups, byName[name])
		}
		byName[name].cs = append(byName[name].cs, c)
	}
	var lines []string
	for _, g := range groups {
		same := !slices.ContainsFunc(g.cs, func(c goldenChange) bool { return c.from != g.cs[0].from || c.to != g.cs[0].to })
		if same {
			rows := make([]string, len(g.cs))
			for k, c := range g.cs {
				rows[k] = c.row
			}
			where := strings.Join(rows, ", ")
			if len(rows) > 4 {
				where = fmt.Sprintf("%s .. %s", rows[0], rows[len(rows)-1])
			}
			lines = append(lines, fmt.Sprintf("  %s: %s -> %s on %d row(s): %s", g.name, g.cs[0].from, g.cs[0].to, len(rows), where))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %d row(s)", g.name, len(g.cs)))
		for _, c := range g.cs {
			lines = append(lines, fmt.Sprintf("      %s  %s -> %s", c.row, c.from, c.to))
		}
	}
	var b strings.Builder
	for k, l := range lines {
		if k == goldenMaxLines {
			fmt.Fprintf(&b, "  ... and %d more lines\n", len(lines)-k)
			break
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// goldenSummary counts what differs: days, workouts, sessions.
func goldenSummary(changes []goldenChange) string {
	rows := map[string]map[string]bool{}
	for _, c := range changes {
		if rows[c.section] == nil {
			rows[c.section] = map[string]bool{}
		}
		rows[c.section][c.row] = true
	}
	var parts []string
	for _, tb := range goldenTables {
		if n := len(rows[tb.section]); n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", tb.section, n))
		}
	}
	return "rows differ: " + strings.Join(parts, ", ")
}

// ---- the update guard --------------------------------------------------------------

// goldenAllowUpdate says whether the files may be made (mode "init": the
// first set, into a directory without any), regenerated (mode "1") or
// extended (mode "additive") with what the run computed; nil if so. m is the
// manifest on disk (nil: none); goldenCommitted has already checked it is
// HEAD's.
func goldenAllowUpdate(mode string, m *goldenManifest, dirHasFiles bool, got map[string]goldenFile, inputs map[string]goldenInputs) error {
	switch mode {
	case "init":
		if m != nil || dirHasFiles {
			return errors.New("init makes the first set into an empty testdata/golden; there are files: use 1 or additive")
		}
		return nil
	case "1", "additive":
	default:
		return fmt.Errorf("%s=%q: use 1, additive or init", goldenUpdate, mode)
	}
	if m == nil {
		if dirHasFiles {
			return errors.New("golden files without manifest.json: restore it from git rather than regenerate blind")
		}
		return errors.New("no golden files yet: the first set needs GOLDEN_MODE=init")
	}
	onlyAdditions := func(name string) error {
		want, err := readGoldenFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // a new user
		}
		if err != nil {
			return err
		}
		for _, c := range goldenChanges(want, got[name], false) {
			if !c.addition() {
				return fmt.Errorf("%s: %s %s.%s moved (%s -> %s)", name, c.row, c.section, c.field, c.from, c.to)
			}
		}
		return nil
	}
	for _, name := range m.Users {
		if _, ok := got[name]; !ok {
			return fmt.Errorf("%s is in the manifest but no longer generated", name)
		}
	}
	if mode == "additive" {
		for _, name := range slices.Sorted(maps.Keys(got)) {
			if err := onlyAdditions(name); err != nil {
				return fmt.Errorf("additive mode refuses: %w", err)
			}
		}
		return nil
	}
	if m.ModelVersion != ModelVersion {
		return nil
	}
	var generator []string
	for name, in := range inputs {
		if was, ok := m.Inputs[name]; !ok || was.Digest != in.Digest {
			generator = append(generator, name)
		}
	}
	slices.Sort(generator)
	if len(generator) == 0 {
		return fmt.Errorf("ModelVersion is still %s, the version the files were made with, and the generator's inputs are unchanged. "+
			"A change that moves a stored number bumps ModelVersion first (version.go, changelog.json, docs/SCORING.md); "+
			"a change that should not move one must pass as it is; new fields only: GOLDEN_MODE=additive", ModelVersion)
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if slices.Contains(generator, name) {
			continue
		}
		if err := onlyAdditions(name); err != nil {
			return fmt.Errorf("the generator changed only %v, but %w: that is a model change", generator, err)
		}
	}
	return nil
}

// goldenCommitted refuses an update while manifest.json differs from HEAD's
// (a regeneration not yet committed, or a hand edit): each update starts from
// committed files, so two in a row cannot stack a model change behind a
// generator change, and the reviewed diff is always against HEAD. head is nil
// when HEAD has no manifest, disk when there is none on disk.
func goldenCommitted(head, disk []byte) error {
	switch {
	case head == nil && disk == nil:
		return nil
	case head == nil:
		return errors.New("manifest.json is not committed yet: commit the files you made (or remove them) before the next update")
	case disk == nil:
		return errors.New("manifest.json is in HEAD but not on disk: git checkout it rather than regenerate blind")
	case !bytes.Equal(head, disk):
		return errors.New("manifest.json differs from HEAD's: commit the last update, or git checkout testdata/golden, before the next one")
	}
	return nil
}

// goldenHEADManifest reads manifest.json as HEAD has it (nil: not in HEAD)
// and as the disk has it (nil: absent). An update needs a git checkout.
func goldenHEADManifest() (head, disk []byte, err error) {
	rel := "./" + filepath.ToSlash(filepath.Join(goldenDir, "manifest.json"))
	if out, err := exec.Command("git", "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, nil, fmt.Errorf("an update needs a git checkout to compare the manifest with HEAD's: %v", err)
	}
	if exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD:"+rel).Run() == nil {
		if head, err = exec.Command("git", "show", "HEAD:"+rel).Output(); err != nil {
			return nil, nil, fmt.Errorf("git show HEAD:%s: %w", rel, err)
		}
	}
	disk, err = os.ReadFile(filepath.Join(goldenDir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return head, nil, nil
	}
	return head, disk, err
}

// ---- the tests that need no database ------------------------------------------------

// TestGoldenManifest: the files must belong to the current ModelVersion (a
// bump without make golden-update fails here), list every user of the
// generator, and hold 30 consecutive days each, scored by that version.
func TestGoldenManifest(t *testing.T) {
	if os.Getenv(goldenUpdate) != "" {
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
	files := map[string]goldenFile{}
	for _, name := range m.Users {
		f, err := readGoldenFile(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		files[name] = f
		if f.User != name || len(f.Days) != m.Days {
			t.Errorf("%s: user %q with %d days, want %d", name, f.User, len(f.Days), m.Days)
			continue
		}
		for k, r := range f.Days {
			if day := addDays(m.FirstDay, k); r["day"] != day || r["model_version"] != m.ModelVersion {
				t.Errorf("%s day %d: %v scored by %v, want %s by %s", name, k, r["day"], r["model_version"], day, m.ModelVersion)
			}
		}
		for _, w := range f.Workouts {
			if v, ok := w["model_version"].(string); ok && v != m.ModelVersion {
				t.Errorf("%s %v: model_version %s", name, w["id"], v)
			}
		}
	}
	if !t.Failed() && goldenFields(files) != m.Fields {
		t.Errorf("the files' field names digest to %s, the manifest says %s: a file edited by hand?", goldenFields(files), m.Fields)
	}
}

// TestGoldenDiffReads pins the failure message's shape and the update guard's
// rules on made-up changes.
func TestGoldenDiffReads(t *testing.T) {
	day := func(d, rec, z string) map[string]any {
		return map[string]any{"day": d, "recovery": json.Number(rec), "model_version": ModelVersion,
			"details": map[string]any{"recovery": map[string]any{"hrv_z": json.Number(z)}}, "zone_minutes": []any{json.Number("1"), json.Number("2")}}
	}
	want := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.41"), day("2026-10-07", "50", "0.2")}}
	got := goldenFile{Days: []map[string]any{day("2026-10-06", "61", "0.3800000001"), day("2026-10-07", "47", "0.2")}}
	changes := goldenChanges(want, got, false)
	report := goldenReport(changes)
	wantReport := "  days.details.recovery.hrv_z: 0.41 -> 0.38 on 1 row(s): 2026-10-06\n" +
		"  days.recovery: 2 row(s)\n" +
		"      2026-10-06  64 -> 61\n" +
		"      2026-10-07  50 -> 47\n"
	if report != wantReport || goldenSummary(changes) != "rows differ: days 2" {
		t.Fatalf("report:\n%s\nwant\n%s(%s)", report, wantReport, goldenSummary(changes))
	}
	if c := goldenChanges(want, want, false); len(c) != 0 {
		t.Fatalf("no change: %v", c)
	}
	same := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.4100000001"), day("2026-10-07", "50", "0.2")}}
	if c := goldenChanges(want, same, false); len(c) != 0 {
		t.Fatalf("a difference below 1e-6 is none: %v", c)
	}
	// A new column is an addition, nothing else, and changes the field digest
	// (but a value does not); a bumped model_version is skipped when asked.
	added := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.41"), day("2026-10-07", "50", "0.2")}}
	added.Days[0]["new_col"] = json.Number("1")
	if c := goldenChanges(want, added, false); len(c) != 1 || !c[0].addition() {
		t.Fatalf("an added column: %+v", c)
	}
	fw := goldenFields(map[string]goldenFile{"u": want})
	if fw == goldenFields(map[string]goldenFile{"u": added}) || fw != goldenFields(map[string]goldenFile{"u": got}) {
		t.Fatal("the field digest must move with a new column and only then")
	}
	bumped := goldenFile{Days: []map[string]any{day("2026-10-06", "64", "0.41"), day("2026-10-07", "50", "0.2")}}
	want.Days[0]["model_version"] = "2026.01.1"
	if c := goldenChanges(want, bumped, true); len(c) != 0 {
		t.Fatalf("model_version after a bump: %+v", c)
	}
	if got := goldenText(map[string]any{"b": json.Number("-0.0000001"), "a": []any{json.Number("1.50"), "x"}}); got != "{\n  \"a\": [1.5, \"x\"],\n  \"b\": 0\n}\n" {
		t.Fatalf("canonical text %q", got)
	}

	// The guard. Without a manifest: files refuse; an empty directory needs init.
	if err := goldenAllowUpdate("1", nil, true, nil, nil); err == nil {
		t.Fatal("files without a manifest must refuse")
	}
	if err := goldenAllowUpdate("1", nil, false, nil, nil); err == nil {
		t.Fatal("the first set must need init")
	}
	if err := goldenAllowUpdate("init", nil, false, nil, nil); err != nil {
		t.Fatalf("init into an empty directory: %v", err)
	}
	m := &goldenManifest{ModelVersion: ModelVersion, Inputs: map[string]goldenInputs{"user-x": {Digest: "1"}}}
	if err := goldenAllowUpdate("init", m, true, nil, nil); err == nil {
		t.Fatal("init over existing files must refuse")
	}
	if err := goldenAllowUpdate("init", nil, true, nil, nil); err == nil {
		t.Fatal("init over files without a manifest must refuse")
	}
	// Same version and inputs refuse; changed inputs are a generator change.
	in := map[string]goldenInputs{"user-x": {Digest: "1"}}
	if err := goldenAllowUpdate("1", m, true, map[string]goldenFile{"user-x": {}}, in); err == nil {
		t.Fatal("same version, same inputs must refuse")
	}
	if err := goldenAllowUpdate("1", m, true, map[string]goldenFile{"user-x": {}}, map[string]goldenInputs{"user-x": {Digest: "2"}}); err != nil {
		t.Fatalf("changed inputs: %v", err)
	}
	if err := goldenAllowUpdate("yes", m, true, nil, nil); err == nil {
		t.Fatal("an unknown mode must refuse")
	}
	// The manifest must be HEAD's.
	committed := []byte(`{"model_version":"x"}`)
	for _, c := range []struct {
		name       string
		head, disk []byte
		ok         bool
	}{
		{"neither: the first set", nil, nil, true},
		{"as committed", committed, committed, true},
		{"made, not committed", nil, committed, false},
		{"deleted", committed, nil, false},
		{"regenerated, not committed", committed, []byte(`{"model_version":"y"}`), false},
	} {
		if err := goldenCommitted(c.head, c.disk); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
