//go:build ignore

// pure_move.go proves that a split of one Go package into more files moved code and changed nothing else.
// It reads the Go files of the package directory before and after, the non-test files and the _test.go files
// as two separate sets (code may move between the files of a set, never between the sets), and compares:
//
//  1. the declarations, by kind, name and package: the exact source text from the doc comment to the end of the
//     last line of the declaration (struct field order, tags, values, body comments in their order, trailing
//     comment). Blank (`var _ I = ...`) and `func init` declarations, of which a package may have several, are
//     compared as a sorted list of texts under one key;
//  2. the comment groups attached to each declaration (doc, trailing, the floating banner before it), the
//     package doc comments, and the comments before the package clause (build constraints, licence headers);
//  3. the imports: the set of (package, local name, path) of the whole set, and, per declaration, the import
//     path and selector each qualifier `x.Sel` resolves to (so an import swapped under the same name is found);
//  4. the set of declarations that are compiled in each of four build contexts (linux/amd64, darwin/arm64,
//     windows/amd64, linux/amd64 with cgo), so a new //go:build line or a _GOOS file name cannot hide a
//     declaration;
//  5. the order of the package-level initialisers that run code (a `var` whose value calls a function or
//     receives from a channel, and every `func init`) in compile order (files sorted by name): two of them that
//     swap places can change what the program does, so the sequence must be the same;
//  6. the multiset of source lines outside package clauses and import declarations (nothing added without a
//     removed twin).
//
// What it does not prove: that the code type-checks (the compiler does that), that initialisation that does not
// run code is order-independent (it is, by the language), and that moved code means the same through the
// imports it uses beyond the qualifier check of 3 (a package whose name differs from its path's last element is
// resolved by the import set of 3 only). Files that are not Go files are compared by check_pure_move.sh.
//
// scripts/check_pure_move.sh runs it for a git ref, together with `go doc -all -u` and `go test -list`.
//
//	go run scripts/pure_move.go <dir-before> <dir-after>
//
// Exit 0: identical. 1: a difference, printed. 2: it cannot work (bad usage, an unparsable file, a comment it
// cannot place). `go run` turns every nonzero exit into 1: build it first (check_pure_move.sh does).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type set struct {
	decls    map[string][]string // package + kind + names -> exact texts, sorted
	usage    map[string][]string // the same key -> "importpath.Sel" a qualifier resolves to, sorted
	comments map[string][]string // the same key -> attached comment groups, raw, sorted
	imports  map[string]bool     // "package localname path"
	headers  []string            // comments before the package clause, with the package doc comments
	ctx      map[string][]string // build context -> "key@hash" of the declarations compiled there, sorted
	initSeq  []string            // order-sensitive initialisers in compile order
	lines    map[string]int      // non-blank lines outside package clauses and imports
	hdrLines int
	files    int
}

func newSet() *set {
	return &set{decls: map[string][]string{}, usage: map[string][]string{}, comments: map[string][]string{},
		imports: map[string]bool{}, ctx: map[string][]string{}, lines: map[string]int{}}
}

var contexts = []struct {
	name string
	ctx  build.Context
}{
	{"linux/amd64", ctxOf("linux", "amd64", false)},
	{"darwin/arm64", ctxOf("darwin", "arm64", false)},
	{"windows/amd64", ctxOf("windows", "amd64", false)},
	{"linux/amd64+cgo", ctxOf("linux", "amd64", true)},
}

func ctxOf(goos, goarch string, cgo bool) build.Context {
	c := build.Default
	c.GOOS, c.GOARCH, c.CgoEnabled = goos, goarch, cgo
	c.BuildTags, c.ToolTags, c.ReleaseTags = nil, nil, build.Default.ReleaseTags
	return c
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pure_move.go <dir-before> <dir-after>")
		os.Exit(2)
	}
	bSrc, bTest, err := scan(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pure_move:", err)
		os.Exit(2)
	}
	aSrc, aTest, err := scan(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pure_move:", err)
		os.Exit(2)
	}
	bad := compare("non-test files", bSrc, aSrc)
	bad = compare("test files", bTest, aTest) || bad
	if bad {
		os.Exit(1)
	}
}

func compare(label string, b, a *set) bool {
	p := "[" + label + "] "
	bad := false
	bad = diffLists(p+"declaration", b.decls, a.decls, true) || bad
	bad = diffLists(p+"attached comments of", b.comments, a.comments, false) || bad
	bad = diffLists(p+"import qualifiers of", b.usage, a.usage, false) || bad
	for _, k := range union(b.imports, a.imports) {
		if b.imports[k] != a.imports[k] {
			side := "added"
			if b.imports[k] {
				side = "removed"
			}
			fmt.Printf("%simport %s: %s\n", p, side, k)
			bad = true
		}
	}
	if !equal(b.headers, a.headers) {
		fmt.Printf("%scomments before the package clause (build constraints, package docs) differ\n--- before\n%s\n--- after\n%s\n",
			p, strings.Join(b.headers, "\n"), strings.Join(a.headers, "\n"))
		bad = true
	}
	for _, c := range contexts {
		bl, al := b.ctx[c.name], a.ctx[c.name]
		if !equal(bl, al) {
			fmt.Printf("%sbuild context %s compiles a different set of declarations (%d before, %d after); first difference: %s\n",
				p, c.name, len(bl), len(al), firstDiff(bl, al))
			bad = true
		}
	}
	if !equal(b.initSeq, a.initSeq) {
		fmt.Printf("%sthe order of the package-level initialisers that run code changed (compile order = files sorted by name); check their dependencies by hand\n--- before\n  %s\n--- after\n  %s\n",
			p, strings.Join(b.initSeq, "\n  "), strings.Join(a.initSeq, "\n  "))
		bad = true
	}
	added, removed := 0, 0
	for _, l := range union(b.lines, a.lines) {
		d := a.lines[l] - b.lines[l]
		switch {
		case d > 0:
			fmt.Printf("%sadded %d line(s) with no removed twin: %q\n", p, d, l)
			added += d
			bad = true
		case d < 0:
			fmt.Printf("%sremoved %d line(s) with no added twin: %q\n", p, -d, l)
			removed -= d
			bad = true
		}
	}
	fmt.Printf("%sfiles: before %d, after %d; declarations: before %d, after %d; init-order-sensitive: %d; imports: %d; identical (exact text, comments, import qualifiers, build contexts, init order): %v\n",
		p, b.files, a.files, count(b.decls), count(a.decls), len(a.initSeq), len(a.imports), !bad)
	fmt.Printf("%slines outside package clauses and imports: before %d, after %d; unmatched added %d, unmatched removed %d; header lines (not compared): before %d, after %d\n",
		p, total(b.lines), total(a.lines), added, removed, b.hdrLines, a.hdrLines)
	return bad
}

func diffLists(what string, b, a map[string][]string, showText bool) bool {
	bad := false
	for _, k := range union(b, a) {
		bl, al := b[k], a[k]
		switch {
		case len(bl) == 0:
			fmt.Printf("%s only after: %s\n", what, k)
			bad = true
		case len(al) == 0:
			fmt.Printf("%s only before: %s\n", what, k)
			bad = true
		case !equal(bl, al):
			fmt.Printf("%s differs: %s\n", what, k)
			if showText {
				fmt.Printf("--- before\n%s\n--- after\n%s\n", strings.Join(bl, "\n\n"), strings.Join(al, "\n\n"))
			}
			bad = true
		}
	}
	return bad
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstDiff(a, b []string) string {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	var ks []string
	for k, v := range m {
		if v != 0 {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return "none"
	}
	return ks[0]
}

func count(m map[string][]string) (n int) {
	for _, v := range m {
		n += len(v)
	}
	return n
}

func total(m map[string]int) (n int) {
	for _, c := range m {
		n += c
	}
	return n
}

func union[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	var ks []string
	for k := range seen {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

// scan reads every Go file of dir (not its subdirectories): the non-test files into one set, the _test.go files
// into the other.
func scan(dir string) (src, test *set, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths) // the order the go tool compiles them in
	src, test = newSet(), newSet()
	for _, p := range paths {
		s := src
		if strings.HasSuffix(p, "_test.go") {
			s = test
		}
		if err := process(p, s); err != nil {
			return nil, nil, err
		}
	}
	if src.files == 0 {
		return nil, nil, fmt.Errorf("%s: no non-test Go files", dir)
	}
	for _, s := range []*set{src, test} {
		for _, m := range []map[string][]string{s.decls, s.usage, s.comments, s.ctx} {
			for k := range m {
				sort.Strings(m[k])
			}
		}
		sort.Strings(s.headers)
	}
	return src, test, nil
}

func process(p string, s *set) error {
	s.files++
	src, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, p, src, parser.ParseComments)
	if err != nil {
		return err
	}
	off := func(pos token.Pos) int { return fs.Position(pos).Offset }
	line := func(pos token.Pos) int { return fs.Position(pos).Line }
	pkg := f.Name.Name

	// Which build contexts compile this file.
	var ctxs []string
	for _, c := range contexts {
		c := c
		ok, err := c.ctx.MatchFile(filepath.Dir(p), filepath.Base(p))
		if err != nil {
			return fmt.Errorf("%s: %v", p, err)
		}
		if ok {
			ctxs = append(ctxs, c.name)
		}
	}

	// Comments before the package clause: the package doc and everything else (build constraints, headers).
	skipLine := map[int]bool{line(f.Package): true}
	for _, g := range f.Comments {
		if g.End() <= f.Package {
			kind := "header"
			if g == f.Doc {
				kind = "package doc"
			}
			s.headers = append(s.headers, fmt.Sprintf("%s %s: %q", pkg, kind, src[off(g.Pos()):off(g.End())]))
		}
	}

	// The names an import gives in this file.
	names := map[string]string{}
	for _, imp := range f.Imports {
		pth, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return fmt.Errorf("%s: %v", p, err)
		}
		nm := ""
		if imp.Name != nil {
			nm = imp.Name.Name
		}
		s.imports[pkg+" "+nm+" "+pth] = true
		switch nm {
		case "_", ".":
			continue
		case "":
			nm = defaultName(pth)
		}
		names[nm] = pth
	}

	cmap := ast.NewCommentMap(fs, f, f.Comments)
	attached := map[*ast.CommentGroup]bool{}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			for l := line(gd.Pos()); l <= line(gd.End()); l++ {
				skipLine[l] = true
			}
			continue
		}
		start := d.Pos()
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
		case *ast.GenDecl:
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
		}
		end := off(d.End())
		for end < len(src) && src[end] != '\n' { // a trailing comment on the last line belongs to it
			end++
		}
		text := string(src[off(start):end])
		k := pkg + " " + key(d)

		s.decls[k] = append(s.decls[k], text)
		for _, c := range ctxs {
			s.ctx[c] = append(s.ctx[c], k+"@"+hash(text))
		}
		quals := map[string]bool{}
		ast.Inspect(d, func(n ast.Node) bool {
			if n == nil {
				return false
			}
			for _, g := range cmap[n] {
				if !attached[g] && g.End() > f.Package { // the ones before the package clause are headers
					attached[g] = true
					s.comments[k] = append(s.comments[k], string(src[off(g.Pos()):off(g.End())]))
				}
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
					if pth, ok := names[id.Name]; ok {
						quals[pth+"."+sel.Sel.Name] = true
					}
				}
			}
			return true
		})
		for q := range quals {
			s.usage[k] = append(s.usage[k], q)
		}
		if sensitive(d) {
			s.initSeq = append(s.initSeq, k+"@"+hash(text))
		}
	}
	for _, g := range f.Comments {
		if !attached[g] && g.End() > f.Package && !skipLine[line(g.Pos())] {
			return fmt.Errorf("%s: a comment group is attached to no declaration: %q", p, g.Text())
		}
	}
	for n, l := range strings.Split(string(src), "\n") {
		switch {
		case skipLine[n+1]:
			if strings.TrimSpace(l) != "" {
				s.hdrLines++
			}
		case strings.TrimSpace(l) != "":
			s.lines[l]++
		}
	}
	return nil
}

var versionSuffix = regexp.MustCompile(`^v[0-9]+$`)
var dotVersion = regexp.MustCompile(`\.v[0-9]+$`)

// defaultName is the name an unnamed import gets, as far as the path says: the last element, the one before a
// /vN suffix, without a .vN suffix.
func defaultName(pth string) string {
	base := path.Base(pth)
	if versionSuffix.MatchString(base) && strings.Contains(pth, "/") {
		base = path.Base(path.Dir(pth))
	}
	base = dotVersion.ReplaceAllString(base, "")
	return strings.ReplaceAll(strings.TrimPrefix(base, "go-"), "-", "_")
}

var builtins = map[string]bool{"len": true, "cap": true, "make": true, "new": true, "append": true, "min": true,
	"max": true, "complex": true, "real": true, "imag": true}

// sensitive reports whether d runs code when the package initialises: `func init`, or a var whose value calls
// something (a builtin does not count) or receives from a channel.
func sensitive(d ast.Decl) bool {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Recv == nil && d.Name.Name == "init"
	case *ast.GenDecl:
		if d.Tok != token.VAR {
			return false
		}
		hit := false
		for _, sp := range d.Specs {
			for _, v := range sp.(*ast.ValueSpec).Values {
				ast.Inspect(v, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.CallExpr:
						if id, ok := n.Fun.(*ast.Ident); !ok || !builtins[id.Name] {
							hit = true
						}
					case *ast.UnaryExpr:
						if n.Op == token.ARROW {
							hit = true
						}
					}
					return !hit
				})
			}
		}
		return hit
	}
	return false
}

func key(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		k := "func "
		if d.Recv != nil && len(d.Recv.List) == 1 {
			k += "(" + exprString(d.Recv.List[0].Type) + ") "
		}
		return k + d.Name.Name
	case *ast.GenDecl:
		var names []string
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		return d.Tok.String() + " " + strings.Join(names, ",")
	}
	return fmt.Sprintf("%T", d)
}

// exprString prints a receiver type: T, *T, T[K].
func exprString(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.IndexExpr:
		return exprString(e.X) + "[" + exprString(e.Index) + "]"
	case *ast.IndexListExpr:
		var parts []string
		for _, i := range e.Indices {
			parts = append(parts, exprString(i))
		}
		return exprString(e.X) + "[" + strings.Join(parts, ",") + "]"
	case *ast.ParenExpr:
		return exprString(e.X)
	}
	return fmt.Sprintf("%T", e)
}
