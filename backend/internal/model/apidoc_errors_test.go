package model

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// docs/API.md → Errors tells the client what to do about every error. Its tables are checked
// against the code here:
//
//   - the `code` table (| `code` | Status | Do |): its first column is exactly the set of Code*
//     constants of this package, each constant is used, and the status in the table is the one
//     the code answers it with (read from the calls of httpx.WriteCodedError and gate.Apply);
//   - the status table (| Status | `code` | When | Do |): it has a row for every status the
//     services write (from the calls and the http.Status* they use) and no row for one they
//     never write, and its `code` column names the same (status, code) pairs;
//   - every row of both has a Do line. That is all the test says about the wording: whether a Do
//     line is right is a review question.
//
// A new Code* constant, a code that is not a model.Code* constant, a new status or a code moved to
// another status fails here until the doc says so. Message texts are not checked here (the doc
// says: branch on the status and the code, not the text); the few it quotes verbatim are checked
// in internal/httpx and internal/gate. Failures name the line of docs/API.md.
//
// Go's test cache does not see docs/API.md (it is outside the module): after editing it run
// `make check-api-doc`, which uses -count=1.

// servedDirs are the packages (relative to backend/) that answer app requests; a status they
// name is one a client can get.
var servedDirs = []string{"cmd/api", "cmd/ingest", "internal/httpx", "internal/auth", "internal/gate"}

// docLine is a line of docs/API.md with its number.
type docLine struct {
	n    int
	text string
}

// errSection is the lines of docs/API.md from "### Errors" to the next "### " heading.
type errSection []docLine

func errDocSection(t *testing.T) errSection {
	t.Helper()
	raw, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatal(err)
	}
	var out errSection
	in := false
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "### ") {
			if in {
				break
			}
			in = l == "### Errors"
		}
		if in {
			out = append(out, docLine{i + 1, l})
		}
	}
	if len(out) == 0 {
		t.Fatal("docs/API.md has no \"### Errors\" section")
	}
	return out
}

func at(n int) string { return fmt.Sprintf("docs/API.md:%d:", n) }

// errRow is a table row: its line in the doc and its trimmed cells.
type errRow struct {
	line  int
	cells []string
}

// errTable returns the rows of the table whose header line starts with header, and the line of
// the header. A row with another number of cells than the header, an empty cell or a missing
// table is an error naming the line.
func errTable(sec errSection, header string) (rows []errRow, headerLine int, err error) {
	for i, l := range sec {
		if !strings.HasPrefix(l.text, header) {
			continue
		}
		n := len(errCells(l.text))
		if i+1 >= len(sec) || !strings.HasPrefix(sec[i+1].text, "| ---") {
			return nil, l.n, fmt.Errorf("%s table %q has no separator line", at(l.n), header)
		}
		for _, r := range sec[i+2:] {
			if !strings.HasPrefix(r.text, "|") {
				break
			}
			c := errCells(r.text)
			if len(c) != n {
				return nil, l.n, fmt.Errorf("%s row has %d cells, the header %d: %.60s", at(r.n), len(c), n, r.text)
			}
			for j, cell := range c {
				if cell == "" {
					return nil, l.n, fmt.Errorf("%s empty cell %d (a Do line is required): %.60s", at(r.n), j+1, r.text)
				}
			}
			rows = append(rows, errRow{r.n, c})
		}
		if len(rows) == 0 {
			return nil, l.n, fmt.Errorf("%s table %q has no rows", at(l.n), header)
		}
		return rows, l.n, nil
	}
	return nil, 0, fmt.Errorf("docs/API.md: no table starting %q in Errors", header)
}

func errCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(strings.TrimSuffix(row, "|"), "|")
	cells := strings.Split(row, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

var backticked = regexp.MustCompile("`([^`]+)`")

// ticks returns the contents of every `...` in s.
func ticks(s string) []string {
	var out []string
	for _, m := range backticked.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// codeConstants parses the non-test files of this package for `Code* = "..."` constants:
// name -> value.
func codeConstants(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Errorf("%s: %s must be a string literal, so that the doc can be checked against it", f, name.Name)
						continue
					}
					out[name.Name], _ = strconv.Unquote(lit.Value)
				}
			}
		}
	}
	return out
}

// httpStatuses is net/http's Status* constants (name -> number), read from the toolchain's source.
func httpStatuses(t *testing.T) map[string]int {
	t.Helper()
	path := filepath.Join(build.Default.GOROOT, "src", "net", "http", "status.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("net/http status codes: %v", err)
	}
	out := map[string]int{}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.INT && strings.HasPrefix(name.Name, "Status") {
					out[name.Name], _ = strconv.Atoi(lit.Value)
				}
			}
		}
	}
	if len(out) < 40 {
		t.Fatalf("read %d status constants from %s, expected the whole list", len(out), path)
	}
	return out
}

// codeUse is a (status, code) pair the services answer with.
type codeUse struct {
	status int
	code   string // the value, "invite_required"
	where  string
}

// served is what the code answers: the statuses >= 400 it writes and the (status, code) pairs.
type served struct {
	statuses map[int]string // status -> first place
	uses     []codeUse
}

// scanServed reads the non-test Go files of backend/cmd and backend/internal.
func scanServed(t *testing.T, consts map[string]string) served {
	t.Helper()
	root := filepath.Join("..", "..") // backend/
	statusNames := httpStatuses(t)
	fset := token.NewFileSet()
	out := served{statuses: map[int]string{}}
	calls := 0
	var applyCodes []codeUse // Apply(…, model.CodeX, …): the status is the one gate.go writes
	gateStatus := 0

	statusOf := func(e ast.Expr) (int, bool) {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind == token.INT {
				n, _ := strconv.Atoi(x.Value)
				return n, true
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" {
				n, ok := statusNames[x.Sel.Name]
				return n, ok
			}
		}
		return 0, false
	}
	codeName := func(e ast.Expr) (string, bool) { // model.CodeX -> CodeX
		if x, ok := e.(*ast.SelectorExpr); ok {
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "model" && strings.HasPrefix(x.Sel.Name, "Code") {
				return x.Sel.Name, true
			}
		}
		return "", false
	}

	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			isServed := false
			for _, s := range servedDirs {
				isServed = isServed || filepath.ToSlash(rel) == s
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr: // every http.StatusX a serving package names
					if st, ok := statusOf(x); ok && isServed && st >= 400 {
						if _, seen := out.statuses[st]; !seen {
							out.statuses[st] = fset.Position(x.Pos()).String()
						}
					}
				case *ast.CallExpr:
					name := ""
					switch f := x.Fun.(type) {
					case *ast.SelectorExpr:
						name = f.Sel.Name
					case *ast.Ident:
						name = f.Name
					}
					pos := fset.Position(x.Pos()).String()
					switch {
					case name == "WriteError" && len(x.Args) == 3, name == "WriteCodedError" && len(x.Args) == 5:
						i := 1
						if name == "WriteCodedError" {
							i = 2
						}
						if st, ok := statusOf(x.Args[i]); ok {
							if _, lit := x.Args[i].(*ast.BasicLit); lit && st >= 400 {
								if _, seen := out.statuses[st]; !seen {
									out.statuses[st] = pos
								}
							}
						}
						if name != "WriteCodedError" {
							break
						}
						calls++
						st, _ := statusOf(x.Args[2])
						if c, ok := codeName(x.Args[3]); ok {
							v, known := consts[c]
							if !known {
								t.Errorf("%s: WriteCodedError with model.%s, which is not a Code* constant of this package", pos, c)
							}
							out.uses = append(out.uses, codeUse{st, v, pos})
						} else if id, ok := x.Args[3].(*ast.Ident); ok && id.Name == "code" && filepath.Base(path) == "gate.go" {
							gateStatus = st // gate.Apply writes its caller's code with this status
						} else {
							t.Errorf("%s: WriteCodedError must be called with a model.Code* constant as its code (a literal or a local constant would escape docs/API.md)", pos)
						}
					case name == "Apply" && len(x.Args) == 7: // gate.Apply(w, r, mode, failed, code, msg, service)
						if c, ok := codeName(x.Args[4]); ok {
							applyCodes = append(applyCodes, codeUse{0, consts[c], pos})
						} else {
							t.Errorf("%s: Apply must be called with a model.Code* constant as its code", pos)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls < 3 { // gate.go's, and the two of cmd/api/train_import.go
		t.Fatalf("found %d calls of WriteCodedError, want at least 3: the scan is broken", calls)
	}
	if len(applyCodes) > 0 && gateStatus == 0 {
		t.Fatal("gate.Apply is called, but internal/gate/gate.go has no WriteCodedError(w, r, <status>, code, msg) to take the status from")
	}
	for _, a := range applyCodes {
		a.status = gateStatus
		out.uses = append(out.uses, a)
	}
	if len(out.statuses) < 8 {
		t.Fatalf("found %d statuses >= 400 in %v, want at least 8: the scan is broken: %v", len(out.statuses), servedDirs, out.statuses)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var oneStatus = regexp.MustCompile("^`([1-5][0-9][0-9])`$")

// TestAPIDocErrorCodesMatchCode: the `code` table of API.md → Errors lists exactly the Code*
// constants, each answered with the status the table gives; the Swift mirror's APIErrorCode has
// the same cases.
func TestAPIDocErrorCodesMatchCode(t *testing.T) {
	consts := codeConstants(t)
	if len(consts) < 4 {
		t.Fatalf("found %d Code* constants, want at least the 4 of v1: %v", len(consts), consts)
	}
	byValue := map[string]string{} // value -> name
	for n, v := range consts {
		byValue[v] = n
	}
	rows, header, err := errTable(errDocSection(t), "| `code` | Status | Do |")
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]int{} // code -> status
	for _, r := range rows {
		c := ticks(r.cells[0])
		m := oneStatus.FindStringSubmatch(r.cells[1])
		if len(c) != 1 || r.cells[0] != "`"+c[0]+"`" || m == nil {
			t.Errorf("%s the first cell must be one `code` and the second one status like `403`: %.60s", at(r.line), strings.Join(r.cells, " | "))
			continue
		}
		if _, dup := doc[c[0]]; dup {
			t.Errorf("%s the code table lists %s twice", at(r.line), c[0])
		}
		doc[c[0]], _ = strconv.Atoi(m[1])
	}
	for _, v := range sortedKeys(byValue) {
		if _, ok := doc[v]; !ok {
			t.Errorf("%s model.%s = %q is not in the `code` table of Errors: add a row with its status and what the client does", at(header), byValue[v], v)
		}
	}
	for _, v := range sortedKeys(doc) {
		if _, ok := byValue[v]; !ok {
			t.Errorf("%s the `code` table lists %q, which no model.Code* constant has", at(header), v)
		}
	}

	// the status each code is answered with
	code := map[string]int{}
	for _, u := range scanServed(t, consts).uses {
		if prev, ok := code[u.code]; ok && prev != u.status {
			t.Errorf("%s: %q is answered with %d here and %d elsewhere; a code has one status", u.where, u.code, u.status, prev)
		}
		code[u.code] = u.status
	}
	for _, v := range sortedKeys(byValue) {
		st, used := code[v]
		switch {
		case !used:
			t.Errorf("model.%s = %q is never sent (no WriteCodedError or gate.Apply uses it): remove it, or send it", byValue[v], v)
		case doc[v] != 0 && doc[v] != st:
			t.Errorf("%s the `code` table gives %q status %d, the code answers it with %d", at(header), v, doc[v], st)
		}
	}

	// the Swift mirror: closed enum with the same raw values
	raw, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)enum APIErrorCode: String, Codable \{(.*?)\n\}`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("docs/API.md: the Swift mirror has no enum APIErrorCode")
	}
	swift := map[string]bool{}
	for _, c := range regexp.MustCompile(`case [^\n]*`).FindAllString(m[1], -1) {
		for _, v := range regexp.MustCompile(`= "([a-z_]+)"`).FindAllStringSubmatch(c, -1) {
			swift[v[1]] = true
		}
	}
	for _, v := range sortedKeys(byValue) {
		if !swift[v] {
			t.Errorf("Swift mirror enum APIErrorCode lacks the case for %q", v)
		}
	}
	for _, v := range sortedKeys(swift) {
		if _, ok := byValue[v]; !ok {
			t.Errorf("Swift mirror enum APIErrorCode has %q, which no model.Code* constant has", v)
		}
	}
}

// TestAPIDocErrorStatuses: the status table has a row, with a Do line, for every status the
// services write and for no other, and its `code` column carries the same (status, code) pairs
// as the code does.
func TestAPIDocErrorStatuses(t *testing.T) {
	sec := errDocSection(t)
	rows, header, err := errTable(sec, "| Status | `code` | When | Do |")
	if err != nil {
		t.Fatal(err)
	}
	sv := scanServed(t, codeConstants(t))

	// statuses the doc says are covered elsewhere: the sentence under the table
	elsewhere := map[int]bool{}
	for _, l := range sec {
		if strings.HasPrefix(l.text, "Endpoint-specific statuses") {
			for _, c := range ticks(l.text) {
				if n, err := strconv.Atoi(c); err == nil && n >= 400 {
					elsewhere[n] = true
				}
			}
		}
	}
	if len(elsewhere) == 0 {
		t.Errorf("%s the sentence \"Endpoint-specific statuses …\" under the status table is gone: it names the statuses (501, 499) the table leaves out", at(header))
	}

	docStatus := map[int]int{} // status -> line
	pairs := map[string]bool{} // "403 invite_required"
	for _, r := range rows {
		m := oneStatus.FindStringSubmatch(r.cells[0])
		if m == nil {
			t.Errorf("%s the first cell must be one status like `403`: %.40s", at(r.line), r.cells[0])
			continue
		}
		st, _ := strconv.Atoi(m[1])
		if _, dup := docStatus[st]; dup {
			t.Errorf("%s the status table lists %d twice", at(r.line), st)
		}
		docStatus[st] = r.line
		for _, c := range ticks(r.cells[1]) {
			pairs[fmt.Sprintf("%d %s", st, c)] = true
		}
	}
	for _, st := range sortedInts(sv.statuses) {
		if _, ok := docStatus[st]; !ok && !elsewhere[st] {
			t.Errorf("%s the services answer %d (%s) and the status table has no row for it: add one with what the client does", at(header), st, sv.statuses[st])
		}
	}
	for st, line := range docStatus {
		if _, ok := sv.statuses[st]; !ok {
			t.Errorf("%s the status table has a row for %d, which no service writes", at(line), st)
		}
	}
	want := map[string]bool{}
	for _, u := range sv.uses {
		want[fmt.Sprintf("%d %s", u.status, u.code)] = true
	}
	for p := range want {
		if !pairs[p] {
			t.Errorf("%s the code answers %q and the `code` column of that status row does not name the code", at(header), p)
		}
	}
	for p := range pairs {
		if !want[p] {
			t.Errorf("%s the `code` column names %q, which the code never answers", at(header), p)
		}
	}
}

func sortedInts[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// TestAPIDocErrorTableCheckerCatchesMistakes: the table reader fails on what it is for, and
// names the line.
func TestAPIDocErrorTableCheckerCatchesMistakes(t *testing.T) {
	sec := func(lines ...string) errSection {
		var s errSection
		for i, l := range lines {
			s = append(s, docLine{100 + i, l})
		}
		return s
	}
	rows, header, err := errTable(sec("| Status | `code` | When | Do |", "| --- | --- | --- | --- |", "| `400` | — | bad | fix it |", "", "after"), "| Status |")
	if err != nil || len(rows) != 1 || rows[0].cells[3] != "fix it" || rows[0].line != 102 || header != 100 {
		t.Fatalf("good table: rows %v, header %d, err %v", rows, header, err)
	}
	for name, c := range map[string]struct {
		lines []string
		line  string
	}{
		"empty Do":      {[]string{"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", "| `400` | — | bad |  |"}, "docs/API.md:102:"},
		"missing cell":  {[]string{"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", "| `400` | bad | fix |"}, "docs/API.md:102:"},
		"no separator":  {[]string{"| Status | `code` | When | Do |", "| `400` | — | bad | fix |"}, "docs/API.md:100:"},
		"no rows":       {[]string{"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", ""}, "docs/API.md:100:"},
		"table is gone": {[]string{"| Other | Table |", "| --- | --- |", "| a | b |"}, "docs/API.md:"},
	} {
		_, _, err := errTable(sec(c.lines...), "| Status |")
		if err == nil || !strings.Contains(err.Error(), c.line) {
			t.Errorf("%s: err = %v, want one naming %s", name, err, c.line)
		}
	}
	if got := ticks("`a` and `b|c`"); len(got) != 2 || got[0] != "a" {
		t.Errorf("ticks = %v", got)
	}
}
