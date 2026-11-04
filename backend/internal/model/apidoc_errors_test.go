package model

import (
	"fmt"
	"go/ast"
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

// docs/API.md → Errors tells the client what to do about every error. Two of its tables are
// pinned to the code here:
//
//   - the `code` table (| `code` | Status | Do |): its first column is exactly the set of Code*
//     constants of this package, and every row has a Do line;
//   - the status table (| Status | `code` | When | Do |): the statuses the services answer with each
//     have a Do line, and its `code` column names only codes of the first table, each under the
//     status that table gives.
//
// A new Code* constant (or a literal code handed to httpx.WriteCodedError, which would bypass the
// constants) fails here until the doc says what the client does about it. Message texts are not
// pinned here (the doc says: branch on the status and the code, not the text); the few it quotes
// verbatim are pinned in internal/httpx and internal/gate.

// errorStatuses are the statuses of the status table. A status the services can answer
// and the table lacks is a gap the client has no instruction for.
var errorStatuses = []string{"400", "401", "403", "404", "409", "413", "422", "429", "500", "503"}

// errDocSection is the text of API.md from "### Errors" to the next "### " heading.
func errDocSection(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "### ") {
			if in {
				break
			}
			in = l == "### Errors"
		}
		if in {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatal("docs/API.md has no \"### Errors\" section")
	}
	return out
}

// errTable returns the rows of the table whose header line starts with header, as trimmed cells.
// A row with another number of cells than the header, an empty cell or a missing table is an error.
func errTable(lines []string, header string) ([][]string, error) {
	for i, l := range lines {
		if !strings.HasPrefix(l, header) {
			continue
		}
		n := len(errCells(l))
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "| ---") {
			return nil, fmt.Errorf("table %q has no separator line", header)
		}
		var rows [][]string
		for _, r := range lines[i+2:] {
			if !strings.HasPrefix(r, "|") {
				break
			}
			c := errCells(r)
			if len(c) != n {
				return nil, fmt.Errorf("table %q: row has %d cells, header %d: %.60s", header, len(c), n, r)
			}
			for j, cell := range c {
				if cell == "" {
					return nil, fmt.Errorf("table %q: empty cell %d in: %.60s", header, j+1, r)
				}
			}
			rows = append(rows, c)
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("table %q has no rows", header)
		}
		return rows, nil
	}
	return nil, fmt.Errorf("no table starting %q", header)
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

// codeConstants parses the non-test files of this package for `Code* = "..."` constants.
func codeConstants(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{} // value -> constant name
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
					v, _ := strconv.Unquote(lit.Value)
					out[v] = name.Name
				}
			}
		}
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

// TestAPIDocErrorCodesMatchCode: the `code` table of API.md → Errors lists exactly the Code*
// constants, each with a status and a Do line; the Swift mirror's APIErrorCode has the same cases.
func TestAPIDocErrorCodesMatchCode(t *testing.T) {
	code := codeConstants(t)
	if len(code) < 4 {
		t.Fatalf("found %d Code* constants, want at least the 4 of v1: %v", len(code), sortedKeys(code))
	}
	rows, err := errTable(errDocSection(t), "| `code` | Status | Do |")
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]string{} // code -> status
	for _, r := range rows {
		c := ticks(r[0])
		if len(c) != 1 || r[0] != "`"+c[0]+"`" {
			t.Errorf("code table: first cell must be one `code`, got %q", r[0])
			continue
		}
		if !regexp.MustCompile("^`[1-5][0-9][0-9]`$").MatchString(r[1]) {
			t.Errorf("code table: %s: status cell must be one status like `403`, got %q", c[0], r[1])
		}
		if _, dup := doc[c[0]]; dup {
			t.Errorf("code table lists %s twice", c[0])
		}
		doc[c[0]] = strings.Trim(r[1], "`")
	}
	for _, v := range sortedKeys(code) {
		if _, ok := doc[v]; !ok {
			t.Errorf("model.%s = %q is not in the `code` table of docs/API.md → Errors: add a row with the status and what the client does", code[v], v)
		}
	}
	for _, v := range sortedKeys(doc) {
		if _, ok := code[v]; !ok {
			t.Errorf("the `code` table of docs/API.md lists %q, which no model.Code* constant has", v)
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
	for _, v := range sortedKeys(code) {
		if !swift[v] {
			t.Errorf("Swift mirror enum APIErrorCode lacks the case for %q", v)
		}
	}
	for _, v := range sortedKeys(swift) {
		if _, ok := code[v]; !ok {
			t.Errorf("Swift mirror enum APIErrorCode has %q, which no model.Code* constant has", v)
		}
	}
}

// TestAPIDocErrorStatuses: every status the services answer has a row with a Do line, and the
// `code` column agrees with the `code` table.
func TestAPIDocErrorStatuses(t *testing.T) {
	sec := errDocSection(t)
	rows, err := errTable(sec, "| Status | `code` | When | Do |")
	if err != nil {
		t.Fatal(err)
	}
	codeRows, err := errTable(sec, "| `code` | Status | Do |")
	if err != nil {
		t.Fatal(err)
	}
	statusOf := map[string]string{}
	for _, r := range codeRows {
		statusOf[strings.Trim(r[0], "`")] = strings.Trim(r[1], "`")
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, r := range rows {
		status := strings.Trim(r[0], "`")
		if seen[status] {
			t.Errorf("status table lists %s twice", status)
		}
		seen[status] = true
		for _, c := range ticks(r[1]) {
			used[c] = true
			want, ok := statusOf[c]
			if !ok {
				t.Errorf("status %s: `code` column names %q, which the `code` table lacks", status, c)
			} else if want != status {
				t.Errorf("status %s: `code` column names %q, whose status in the `code` table is %s", status, c, want)
			}
		}
	}
	for _, s := range errorStatuses {
		if !seen[s] {
			t.Errorf("the status table of docs/API.md → Errors has no row for %s", s)
		}
	}
	for c := range statusOf {
		if !used[c] {
			t.Errorf("code %q is in the `code` table but in no row's `code` column of the status table", c)
		}
	}
}

// TestAPIDocErrorTableCheckerCatchesMistakes: the checks above fail on what they are for.
func TestAPIDocErrorTableCheckerCatchesMistakes(t *testing.T) {
	good := []string{"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", "| `400` | — | bad | fix it |", "", "after"}
	if rows, err := errTable(good, "| Status |"); err != nil || len(rows) != 1 || rows[0][3] != "fix it" {
		t.Fatalf("good table: rows %v, err %v", rows, err)
	}
	for name, lines := range map[string][]string{
		"empty Do":      {"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", "| `400` | — | bad |  |"},
		"missing cell":  {"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", "| `400` | bad | fix |"},
		"no separator":  {"| Status | `code` | When | Do |", "| `400` | — | bad | fix |"},
		"no rows":       {"| Status | `code` | When | Do |", "| --- | --- | --- | --- |", ""},
		"table is gone": {"| Other | Table |", "| --- | --- |", "| a | b |"},
	} {
		if _, err := errTable(lines, "| Status |"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if got := ticks("`a` and `b|c`"); len(got) != 2 || got[0] != "a" {
		t.Errorf("ticks = %v", got)
	}
}

// TestNoLiteralErrorCodes: httpx.WriteCodedError is called with a model.Code* constant, never with
// a string literal, so the set of constants is the set of codes the services can send. (gate.Apply
// takes its code from its caller, which passes a constant; the test below covers it too.)
func TestNoLiteralErrorCodes(t *testing.T) {
	root := filepath.Join("..", "..") // backend/
	fset := token.NewFileSet()
	calls := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = f.Sel.Name
				case *ast.Ident:
					name = f.Name
				}
				switch name {
				case "WriteCodedError": // (w, r, status, code, msg)
					calls++
					if len(call.Args) == 5 {
						if _, lit := call.Args[3].(*ast.BasicLit); lit {
							t.Errorf("%s: WriteCodedError with a literal code; use a model.Code* constant", fset.Position(call.Pos()))
						}
					}
				case "Apply": // gate.Apply(w, r, mode, failed, code, msg, service)
					if len(call.Args) == 7 {
						if _, lit := call.Args[4].(*ast.BasicLit); lit {
							t.Errorf("%s: Apply with a literal code; use a model.Code* constant", fset.Position(call.Pos()))
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
		t.Errorf("found %d calls of WriteCodedError, want at least 3: the scan is broken", calls)
	}
}
