package internal_test

// The architecture of the module as tests: which package may import which, and
// the shape of every package's documentation. The directory has no non-test Go
// file on purpose (so it needs no doc.go and adds no statements to coverage).
//
// The import rules read the non-test import graph from `go list`. A rule that is
// already enforced elsewhere is not repeated here: the compiler owns "cmd/*
// never import each other" (a package main cannot be imported) and depguard owns
// "testsupport and logx/logcheck are for tests only" (.golangci.yml). Each rule
// names itself, the offending edge and why the rule exists, so a failure says
// what to fix; to allow an edge, add an allowed entry with a reason, do not
// loosen the pattern.
//
// Package names in the tables are relative to the module (internal/store,
// cmd/api); an import of anything else keeps its full path. A pattern ending in
// "/..." matches that package and everything below it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// maxSynopsis is the longest first sentence of a doc.go, because
// scripts/pkgmap.sh puts it into a table cell.
const maxSynopsis = 160

// node is one package of the module, with its imports trimmed to module-relative
// names.
type node struct {
	Path    string // module-relative: internal/store
	Dir     string
	Imports []string // non-test imports
	Files   []string // non-test Go files, base names
	Tests   []string // _test.go files, base names (both the package's and its external ones)
}

type graph map[string]node

// denyRule: the packages in From may not import anything matching Deny, except
// what matches Except.
type denyRule struct {
	ID, Why string
	From    []string
	Deny    []string
	Except  []string
}

// onlyRule: anything matching Target may be imported by the listed packages only.
type onlyRule struct {
	ID, Why   string
	Target    []string
	Importers []allowed
}

type allowed struct {
	Pkg    string
	Reason string // why this importer is on the list, when it is not the rule's own idea
}

var internalAll = []string{"internal/..."}

var denyRules = []denyRule{
	{
		ID: "R1", Why: "the wire types are the contract and import nothing of ours (docs/API.md; internal/model/doc.go)",
		From: []string{"internal/model"}, Deny: internalAll,
	},
	{
		// scoring/workoutdup is the one package below scoring that store may use: it
		// imports model only, so the store can call the pure fold rule (D146).
		ID: "R2", Why: "store holds SQL and calls pure rules; it does not know the engine, the edge, the transport or the push path (D101, D146)",
		From: []string{"internal/store"},
		Deny: []string{
			"internal/scoring/...", "internal/notify", "internal/httpx", "internal/auth", "internal/gate",
			"internal/eventbus", "internal/events/amqpbus", "internal/apns", "internal/apple",
		},
		Except: []string{"internal/scoring/workoutdup"},
	},
	{
		ID: "R4", Why: "every rule is a pure function: no network, file, database, broker or metrics code, and no internal package but model and train (CLAUDE.md: every rule is a pure function; D146 for workoutdup)",
		From: []string{
			"internal/model", "internal/flags", "internal/insights", "internal/behavior", "internal/train",
			"internal/scoring/workoutdup",
		},
		Deny: []string{
			"net", "net/...", "os", "os/...", "io/fs", "syscall", "embed", "log", "log/...", "database/...",
			"github.com/jackc/...", "github.com/rabbitmq/...", "github.com/prometheus/...", "internal/...",
		},
		Except: []string{"internal/model", "internal/train"},
	},
	{
		ID: "R6", Why: "the outbox sends through events.Sender and knows no transport or table (D100, D101)",
		From: []string{"internal/events", "internal/events/outbox"},
		Deny: []string{"internal/events/amqpbus", "internal/eventbus", "internal/store", "internal/db"},
	},
	{
		ID: "R8", Why: "the edge stays storage-free; the user row arrives through OnAuth (docs/ARCHITECTURE.md Request path; internal/httpx/doc.go)",
		From: []string{"internal/auth", "internal/httpx", "internal/gate"},
		Deny: []string{"internal/store", "internal/db", "internal/events/..."},
	},
	{
		ID: "R7", Why: "internal/app holds the pool and nothing else of the driver: SQL stays in store, db and the outbox",
		From: []string{"internal/app"}, Deny: []string{"github.com/jackc/..."},
		Except: []string{"github.com/jackc/pgx/v5/pgxpool"},
	},
	{
		ID: "R9", Why: "leaf packages import nothing of ours, so cycles stay impossible (each one's doc.go)",
		From: []string{
			"internal/config", "internal/logx", "internal/logx/logcheck", "internal/metrics", "internal/retry",
			"internal/ratelimit", "internal/sealed", "internal/testsupport",
		},
		Deny: internalAll,
	},
}

var onlyRules = []onlyRule{
	{
		ID: "R5", Why: "services see events.Bus; the transport stays swappable (D97, D102)",
		Target: []string{"internal/events/amqpbus"},
		Importers: []allowed{
			{Pkg: "internal/eventbus"},
			{Pkg: "cmd/rabbitmq-topology", Reason: "writes the generated topology (D99)"},
		},
	},
	{
		ID: "R5", Why: "only the transport package speaks AMQP (D97, D102)",
		Target:    []string{"github.com/rabbitmq/amqp091-go"},
		Importers: []allowed{{Pkg: "internal/events/amqpbus"}},
	},
	{
		ID: "R7", Why: "SQL lives in store, db and the outbox, so pools stay behind them (docs/ARCHITECTURE.md Data model; internal/store/doc.go)",
		Target: []string{"github.com/jackc/pgx/v5/pgxpool"},
		Importers: []allowed{
			{Pkg: "internal/db"}, {Pkg: "internal/store"}, {Pkg: "internal/events/outbox"},
			{Pkg: "internal/app", Reason: "opens and closes the services' pool and hands it to Build (services start and stop through internal/app)"},
		},
	},
	{
		ID: "R7", Why: "SQL lives in store, db and the outbox (docs/ARCHITECTURE.md Data model); a driver type elsewhere needs a reason",
		Target: []string{"github.com/jackc/..."},
		Importers: []allowed{
			{Pkg: "internal/db"}, {Pkg: "internal/store"}, {Pkg: "internal/events/outbox"},
			{Pkg: "internal/testsupport", Reason: "test helper that opens the throwaway database"},
			{Pkg: "cmd/api", Reason: "pgx.Tx for the outbox callback (D101)"},
			{Pkg: "cmd/ingest", Reason: "pgx.Tx for the outbox callback (D101) and pgconn.PgError codes in fail"},
			{Pkg: "internal/app", Reason: "the pgxpool.Pool it opens and closes; no other pgx type (the deny rule below)"},
		},
	},
	{
		ID: "R10", Why: "DATA_ENCRYPTION_KEY exists only in api's namespace (D48)",
		Target:    []string{"internal/sealed"},
		Importers: []allowed{{Pkg: "cmd/api"}},
	},
	{
		ID: "R13", Why: "app owns a service's lifecycle; subcommands and migrate are one-shot and stay outside it (services start and stop through internal/app)",
		Target: []string{"internal/app"},
		Importers: []allowed{
			{Pkg: "cmd/api"}, {Pkg: "cmd/ingest"}, {Pkg: "cmd/scoring"}, {Pkg: "cmd/notify"},
		},
	},
}

// match reports whether import path p matches the pattern.
func match(pattern, p string) bool {
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return p == base || strings.HasPrefix(p, base+"/")
	}
	return p == pattern
}

func matchAny(patterns []string, p string) bool {
	return slices.ContainsFunc(patterns, func(pat string) bool { return match(pat, p) })
}

// checkImports applies the rules to the graph and returns one message per
// offending edge.
func checkImports(g graph, denies []denyRule, onlys []onlyRule) []string {
	var out []string
	for _, p := range sortedPaths(g) {
		n := g[p]
		for _, r := range denies {
			if !matchAny(r.From, p) {
				continue
			}
			for _, imp := range n.Imports {
				if matchAny(r.Deny, imp) && !matchAny(r.Except, imp) {
					out = append(out, fmt.Sprintf("%s: %s imports %s. %s", r.ID, p, imp, r.Why))
				}
			}
		}
		for _, r := range onlys {
			for _, imp := range n.Imports {
				if !matchAny(r.Target, imp) {
					continue
				}
				if !slices.ContainsFunc(r.Importers, func(a allowed) bool { return match(a.Pkg, p) }) {
					out = append(out, fmt.Sprintf("%s: %s imports %s, which only %s may import. %s (to allow it, add an entry with a reason)",
						r.ID, p, imp, importerNames(r.Importers), r.Why))
				}
			}
		}
	}
	return out
}

// staleRules names the patterns of a rule that match nothing any more, so a
// rename or a major-version bump (internal/events, pgx/v6, amqp091-go/v2) cannot
// leave a rule that checks nothing. A pattern into this module or a dependency
// (anything but the standard library) must match a package of the module or
// something a package of the module imports.
func staleRules(g graph, denies []denyRule, onlys []onlyRule) []string {
	var known []string
	for p, n := range g {
		known = append(known, p)
		known = append(known, n.Imports...)
	}
	var out []string
	check := func(id string, pats []string) {
		for _, pat := range pats {
			first, _, _ := strings.Cut(pat, "/")
			if first != "internal" && first != "cmd" && !strings.Contains(first, ".") {
				continue // the standard library cannot be renamed
			}
			if !slices.ContainsFunc(known, func(p string) bool { return match(pat, p) }) {
				out = append(out, fmt.Sprintf("%s names %s, which matches no package of the module and nothing it imports (renamed, bumped or removed?)", id, pat))
			}
		}
	}
	for _, r := range denies {
		check(r.ID, r.From)
		check(r.ID, r.Deny)
		check(r.ID, r.Except)
	}
	for _, r := range onlys {
		check(r.ID, r.Target)
		for _, a := range r.Importers {
			check(r.ID, []string{a.Pkg})
		}
	}
	return out
}

func importerNames(as []allowed) string {
	names := make([]string, len(as))
	for i, a := range as {
		names[i] = a.Pkg
	}
	return strings.Join(names, ", ")
}

func sortedPaths(g graph) []string {
	paths := make([]string, 0, len(g))
	for p := range g {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
}

var (
	loadOnce sync.Once
	loaded   graph
	loadErr  error
)

// loadGraph reads the module's packages with `go list`, for one fixed platform
// so the file sets do not depend on the machine running the test.
func loadGraph(t *testing.T) graph {
	t.Helper()
	loadOnce.Do(func() { loaded, loadErr = goList() })
	if loadErr != nil {
		t.Fatalf("go list: %v", loadErr)
	}
	return loaded
}

func goList() (graph, error) {
	goBin := "go" // go test puts the toolchain that runs it first on PATH
	// A developer's go.work or GOFLAGS (-tags) must not change the file sets.
	env := append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	envCmd := exec.Command(goBin, "env", "GOMOD")
	envCmd.Env = env
	gomod, err := envCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go env GOMOD: %w", err)
	}
	root := filepath.Dir(strings.TrimSpace(string(gomod)))

	cmd := exec.Command(goBin, "list", "-json=ImportPath,Dir,Imports,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Module", "./...")
	cmd.Dir = root
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	g := graph{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p struct {
			ImportPath                 string
			Dir                        string
			Imports, GoFiles, CgoFiles []string
			TestGoFiles, XTestGoFiles  []string
			Module                     struct{ Path string }
		}
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		rel := func(s string) string { return strings.TrimPrefix(s, p.Module.Path+"/") }
		n := node{Path: rel(p.ImportPath), Dir: p.Dir, Files: append(p.GoFiles, p.CgoFiles...)}
		if p.ImportPath == p.Module.Path {
			n.Path = "."
		}
		for _, imp := range p.Imports {
			n.Imports = append(n.Imports, rel(imp))
		}
		n.Tests = append(append(n.Tests, p.TestGoFiles...), p.XTestGoFiles...)
		g[n.Path] = n
	}
	if len(g) == 0 {
		return nil, fmt.Errorf("no packages listed in %s", root)
	}
	return g, nil
}

func TestImportRules(t *testing.T) {
	g := loadGraph(t)
	for _, msg := range staleRules(g, denyRules, onlyRules) {
		t.Error(msg)
	}
	for _, msg := range checkImports(g, denyRules, onlyRules) {
		t.Error(msg)
	}
}

// nolintProblem says what is wrong with a comment if it is a //nolint directive
// that could silence revive (it names revive, names all, or names nothing, which
// silences every linter); "" otherwise. golangci's nolintlint (require-specific)
// refuses the bare form too; this keeps the check independent of that setting.
func nolintProblem(text string) string {
	rest, ok := strings.CutPrefix(strings.TrimLeft(strings.TrimPrefix(text, "//"), " \t"), "nolint")
	if !ok || !strings.HasPrefix(text, "//") {
		return ""
	}
	switch {
	case rest == "" || rest[0] == ' ' || rest[0] == '\t':
		return "a bare //nolint silences revive with everything else"
	case rest[0] != ':':
		return "" // a word that only starts with nolint
	}
	list, _, _ := strings.Cut(rest[1:], " ")
	for _, l := range strings.Split(list, ",") {
		if l == "revive" || l == "all" {
			return "//nolint:" + l + " silences revive"
		}
	}
	return ""
}

// docProblems checks the documentation shape of one package: exactly one doc.go,
// holding the only package comment and no declaration, whose first paragraph is
// one sentence of at most maxSynopsis characters; and no //nolint that could
// silence revive anywhere in the package (directives are read from the parsed
// comments, so a string or a comment that merely quotes one does not count).
func docProblems(n node) []string {
	var out []string
	fset := token.NewFileSet()
	var withComment []string
	hasDocGo := false
	for _, f := range n.Files {
		if f == "doc.go" {
			hasDocGo = true
		}
	}
	for _, f := range n.Files {
		path := filepath.Join(n.Dir, f)
		af, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			out = append(out, fmt.Sprintf("R12: %s: %v", n.Path, err))
			continue
		}
		if af.Doc == nil {
			continue
		}
		withComment = append(withComment, f)
		if f != "doc.go" {
			continue
		}
		if len(af.Decls) > 0 {
			out = append(out, fmt.Sprintf("R12: %s/doc.go holds a declaration; it is the package comment and the package clause only", n.Path))
		}
		text := af.Doc.Text()
		syn := new(doc.Package).Synopsis(text)
		para, _, _ := strings.Cut(text, "\n\n")
		para = strings.Join(strings.Fields(para), " ")
		switch {
		case syn == "":
			out = append(out, fmt.Sprintf("R12: %s/doc.go has no first sentence", n.Path))
		case para != syn:
			out = append(out, fmt.Sprintf("R12: %s/doc.go: the first paragraph must be one sentence ending in a period (it is the \"owns\" cell); the first sentence is %q, the paragraph %q", n.Path, syn, para))
		case !strings.HasSuffix(syn, "."):
			out = append(out, fmt.Sprintf("R12: %s/doc.go: the first sentence must end in a period, not %q", n.Path, syn))
		}
		if l := utf8.RuneCountInString(syn); l > maxSynopsis {
			out = append(out, fmt.Sprintf("R12: %s/doc.go: the first sentence is %d characters, the package map allows %d (it is the \"owns\" cell): %q",
				n.Path, l, maxSynopsis, syn))
		}
	}
	switch {
	case !hasDocGo:
		out = append(out, fmt.Sprintf("R12: %s has no doc.go; every package with Go files has one, holding the only package comment", n.Path))
	case len(withComment) == 0:
		out = append(out, fmt.Sprintf("R12: %s/doc.go has no package comment", n.Path))
	}
	if len(withComment) > 1 || len(withComment) == 1 && withComment[0] != "doc.go" {
		out = append(out, fmt.Sprintf("R12: %s has a package comment in %s; doc.go holds the only one",
			n.Path, strings.Join(withComment, ", ")))
	}
	for _, f := range append(slices.Clone(n.Files), n.Tests...) {
		af, err := parser.ParseFile(fset, filepath.Join(n.Dir, f), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			out = append(out, fmt.Sprintf("R12: %s: %v", n.Path, err))
			continue
		}
		for _, cg := range af.Comments {
			for _, c := range cg.List {
				if why := nolintProblem(c.Text); why != "" {
					out = append(out, fmt.Sprintf("R12: %s/%s:%d: %s; the exported-comment budget is zero, write the comment (and name the linter in every //nolint)",
						n.Path, f, fset.Position(c.Pos()).Line, why))
				}
			}
		}
	}
	return out
}

func TestPackageDocs(t *testing.T) {
	g := loadGraph(t)
	for _, p := range sortedPaths(g) {
		n := g[p]
		if len(n.Files) == 0 {
			continue // a test-only directory, like this one, has no package to document
		}
		for _, msg := range docProblems(n) {
			t.Error(msg)
		}
	}
}

// TestArchCheckersCatchMistakes feeds the checkers graphs and packages that break
// each rule, so a refactor of the checkers cannot turn them into no-ops.
func TestArchCheckersCatchMistakes(t *testing.T) {
	g := graph{
		"internal/model":              {Path: "internal/model", Imports: []string{"internal/store", "encoding/json"}},
		"internal/store":              {Path: "internal/store", Imports: []string{"internal/scoring/workoutdup", "internal/scoring", "internal/httpx"}},
		"internal/scoring/workoutdup": {Path: "internal/scoring/workoutdup", Imports: []string{"internal/model", "os", "internal/events"}},
		"internal/events":             {Path: "internal/events", Imports: []string{"internal/events/amqpbus"}},
		"internal/auth":               {Path: "internal/auth", Imports: []string{"internal/db"}},
		"internal/config":             {Path: "internal/config", Imports: []string{"internal/model"}},
		"internal/notify":             {Path: "internal/notify", Imports: []string{"internal/events/amqpbus", "internal/sealed", "github.com/jackc/pgx/v5/pgxpool", "github.com/jackc/pgx/v5", "internal/app"}},
		"cmd/api":                     {Path: "cmd/api", Imports: []string{"internal/sealed", "github.com/jackc/pgx/v5"}},
		"internal/app":                {Path: "internal/app", Imports: []string{"github.com/jackc/pgx/v5/pgxpool", "github.com/jackc/pgx/v5"}},
	}
	want := []string{
		"R1: internal/model imports internal/store",
		"R4: internal/model imports internal/store",
		"R2: internal/store imports internal/scoring.",
		"R2: internal/store imports internal/httpx",
		"R4: internal/scoring/workoutdup imports os.",
		"R4: internal/scoring/workoutdup imports internal/events.",
		"R6: internal/events imports internal/events/amqpbus",
		"R5: internal/events imports internal/events/amqpbus, which only",
		"R8: internal/auth imports internal/db",
		"R9: internal/config imports internal/model",
		"R5: internal/notify imports internal/events/amqpbus, which only internal/eventbus, cmd/rabbitmq-topology may import",
		"R10: internal/notify imports internal/sealed, which only cmd/api may import",
		"R7: internal/notify imports github.com/jackc/pgx/v5/pgxpool, which only internal/db, internal/store, internal/events/outbox, internal/app may import",
		"R7: internal/notify imports github.com/jackc/pgx/v5/pgxpool, which only internal/db, internal/store, internal/events/outbox, internal/testsupport",
		"R7: internal/notify imports github.com/jackc/pgx/v5,",
		"R7: internal/app imports github.com/jackc/pgx/v5.",
		"R13: internal/notify imports internal/app, which only cmd/api, cmd/ingest, cmd/scoring, cmd/notify may import",
	}
	got := checkImports(g, denyRules, onlyRules)
	for _, w := range want {
		if !slices.ContainsFunc(got, func(m string) bool { return strings.HasPrefix(m, w) }) {
			t.Errorf("no violation starting %q in:\n%s", w, strings.Join(got, "\n"))
		}
	}
	for _, m := range got {
		for _, bad := range []string{"internal/store imports internal/scoring/workoutdup", "cmd/api imports"} {
			if strings.Contains(m, bad) {
				t.Errorf("allowed edge reported: %s", m)
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d violations, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}

	if s := staleRules(graph{"internal/model": {}}, denyRules[:1], nil); len(s) != 0 {
		t.Errorf("stale rule reported for a package that exists: %v", s)
	}
	if s := staleRules(graph{}, denyRules[:1], nil); len(s) == 0 {
		t.Error("a rule naming a missing package is not reported")
	}
	// A bumped dependency or a renamed subtree leaves a rule that matches nothing.
	bumped := graph{"internal/store": {Path: "internal/store", Imports: []string{"github.com/jackc/pgx/v6/pgxpool", "github.com/rabbitmq/amqp091-go/v2"}}}
	for _, r := range []onlyRule{onlyRules[1], onlyRules[2]} {
		if s := staleRules(bumped, nil, []onlyRule{r}); !slices.ContainsFunc(s, func(m string) bool { return strings.Contains(m, r.Target[0]) }) {
			t.Errorf("a target that no package imports (%s) is not reported: %v", r.Target[0], s)
		}
	}
	if s := staleRules(graph{"internal/model": {}}, []denyRule{{ID: "RX", From: []string{"internal/model"}, Deny: []string{"internal/events/..."}}}, nil); len(s) == 0 {
		t.Error("a /... pattern that matches no package is not reported")
	}
	current := graph{"internal/store": {Path: "internal/store", Imports: []string{"github.com/jackc/pgx/v5/pgxpool", "database/sql"}}}
	if s := staleRules(current, nil, []onlyRule{{ID: "RX", Target: []string{"github.com/jackc/pgx/v5/pgxpool"}, Importers: []allowed{{Pkg: "internal/store"}}}}); len(s) != 0 {
		t.Errorf("a current rule is reported: %v", s)
	}
	if s := staleRules(graph{}, []denyRule{{ID: "RX", Deny: []string{"database/...", "net"}}}, nil); len(s) != 0 {
		t.Errorf("a standard library pattern is reported: %v", s)
	}

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long := "// Package x " + strings.Repeat("owns a very long sentence ", 8) + "that never ends.\npackage x\n"
	write("doc.go", long)
	write("a.go", "// Package x is described twice.\npackage x\n\nvar A = 1 //nolint:gosec,revive // no\n")
	got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"doc.go", "a.go"}})
	for _, w := range []string{"the first sentence is", "package comment in doc.go, a.go", "a.go:4: //nolint:revive silences revive"} {
		if !slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, w) }) {
			t.Errorf("no doc problem containing %q in:\n%s", w, strings.Join(got, "\n"))
		}
	}

	// Directives are read from comments: bare and all are refused, a string that quotes one is not.
	write("doc.go", "// Package x owns y.\npackage x\n")
	write("a.go", "package x\n\nvar A = 1 //nolint\nvar B = 2 //nolint:all // x\nvar C = \"//nolint:revive\"\nvar D = 3 //nolint:gosec // fine\n")
	got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"doc.go", "a.go"}})
	if len(got) != 2 || !strings.Contains(got[0], "a.go:3: a bare //nolint") || !strings.Contains(got[1], "a.go:4: //nolint:all") {
		t.Errorf("bare and all: %v", got)
	}
	write("doc.go", "// Package x owns y. It also does z.\npackage x\n")
	write("a.go", "package x\n")
	got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"doc.go", "a.go"}})
	if len(got) != 1 || !strings.Contains(got[0], "one sentence") {
		t.Errorf("two sentences in the first paragraph: %v", got)
	}

	write("doc.go", "// Package x owns y.\npackage x\n\nvar Z = 1\n")
	write("a.go", "package x\n")
	got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"doc.go", "a.go"}})
	if len(got) != 1 || !strings.Contains(got[0], "holds a declaration") {
		t.Errorf("declaration in doc.go: %v", got)
	}
	got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"a.go"}})
	if len(got) != 1 || !strings.Contains(got[0], "has no doc.go") {
		t.Errorf("missing doc.go: %v", got)
	}

	write("doc.go", "// Package x is run as:\n//\n//\tgo run x\npackage x\n")
	got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"doc.go", "a.go"}})
	if len(got) != 1 || !strings.Contains(got[0], "end in a period") {
		t.Errorf("a first paragraph that is not a sentence: %v", got)
	}

	write("doc.go", "// Package x owns y.\npackage x\n")
	if got = docProblems(node{Path: "internal/x", Dir: dir, Files: []string{"doc.go", "a.go"}}); len(got) != 0 {
		t.Errorf("a correct package is reported: %v", got)
	}
}
