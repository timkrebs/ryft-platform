//go:build ignore

// pure_move.go proves that a split of one Go package into more files moved code and changed nothing else.
// It reads the non-test files of the package directory before and after and compares:
//
//  1. the set of top-level declarations: kind, name, the printed AST without comments (struct field
//     order, tags, values) and the comment groups attached to each declaration (doc, trailing, floating
//     before it), whichever file it lives in;
//  2. the multiset of source lines outside the package clauses and the import declarations: every blank-free
//     line that was added must exist as a removed line, and the other way round (the diff of a pure move
//     contains nothing but moved blocks and file headers).
//
// scripts/check_pure_move.sh runs it for a git ref, together with `go doc -all -u` and `go test -list`.
//
//	go run scripts/pure_move.go <dir-before> <dir-after>
//
// Exit 0: identical. 1: a difference, printed. 2: bad usage or an unparsable file.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pure_move.go <dir-before> <dir-after>")
		os.Exit(2)
	}
	bDecls, bLines, bHdr, err := scan(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pure_move:", err)
		os.Exit(2)
	}
	aDecls, aLines, aHdr, err := scan(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "pure_move:", err)
		os.Exit(2)
	}
	bad := false
	for _, k := range union(bDecls, aDecls) {
		switch {
		case bDecls[k] == "":
			fmt.Printf("only after: %s\n", k)
			bad = true
		case aDecls[k] == "":
			fmt.Printf("only before: %s\n", k)
			bad = true
		case bDecls[k] != aDecls[k]:
			fmt.Printf("differs: %s\n--- before\n%s\n--- after\n%s\n", k, bDecls[k], aDecls[k])
			bad = true
		}
	}
	added, removed := 0, 0
	for _, l := range union(bLines, aLines) {
		d := aLines[l] - bLines[l]
		switch {
		case d > 0:
			fmt.Printf("added %d line(s) with no removed twin: %q\n", d, l)
			added += d
			bad = true
		case d < 0:
			fmt.Printf("removed %d line(s) with no added twin: %q\n", -d, l)
			removed -= d
			bad = true
		}
	}
	fmt.Printf("declarations: before %d, after %d; every one identical (printed AST, field order, tags, comments): %v\n",
		len(bDecls), len(aDecls), !bad)
	fmt.Printf("lines outside package clauses and imports: before %d, after %d; unmatched added %d, unmatched removed %d\n",
		total(bLines), total(aLines), added, removed)
	fmt.Printf("header lines (package clause and imports, not compared): before %d, after %d\n", bHdr, aHdr)
	if bad {
		os.Exit(1)
	}
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

// scan returns the declarations (key to printed AST plus comments), the multiset of non-blank source lines
// outside the package clause and the import declarations, and the number of those header lines.
func scan(dir string) (decls map[string]string, lines map[string]int, headers int, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, nil, 0, err
	}
	sort.Strings(paths)
	decls = map[string]string{}
	lines = map[string]int{}
	nfiles := 0
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		nfiles++
		src, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, 0, err
		}
		fsPlain := token.NewFileSet()
		plain, err := parser.ParseFile(fsPlain, p, src, 0) // no comments: the printed AST carries none
		if err != nil {
			return nil, nil, 0, err
		}
		fsC := token.NewFileSet()
		withC, err := parser.ParseFile(fsC, p, src, parser.ParseComments)
		if err != nil {
			return nil, nil, 0, err
		}
		cmap := ast.NewCommentMap(fsC, withC, withC.Comments)
		attached := map[*ast.CommentGroup]bool{}
		skip := map[int]bool{fsC.Position(withC.Package).Line: true}
		for i, d := range withC.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				for l := fsC.Position(g.Pos()).Line; l <= fsC.Position(g.End()).Line; l++ {
					skip[l] = true
				}
				continue
			}
			var buf bytes.Buffer
			if err := printer.Fprint(&buf, fsPlain, plain.Decls[i]); err != nil {
				return nil, nil, 0, err
			}
			var cs []string
			ast.Inspect(d, func(n ast.Node) bool {
				if n == nil {
					return false
				}
				for _, g := range cmap[n] {
					if !attached[g] {
						attached[g] = true
						cs = append(cs, fmt.Sprintf("%q", string(src[fsC.Position(g.Pos()).Offset:fsC.Position(g.End()).Offset])))
					}
				}
				return true
			})
			sort.Strings(cs)
			k := key(plain.Decls[i])
			if _, dup := decls[k]; dup {
				return nil, nil, 0, fmt.Errorf("%s: %s is declared twice", p, k)
			}
			decls[k] = buf.String() + "\n// comments:\n" + strings.Join(cs, "\n")
		}
		if withC.Doc != nil {
			attached[withC.Doc] = true
		}
		for _, g := range withC.Comments {
			if !attached[g] && !inRange(fsC, g, skip) {
				return nil, nil, 0, fmt.Errorf("%s: a comment group is attached to no declaration: %q", p, g.Text())
			}
		}
		for n, l := range strings.Split(string(src), "\n") {
			switch {
			case skip[n+1]:
				if strings.TrimSpace(l) != "" {
					headers++
				}
			case strings.TrimSpace(l) != "":
				lines[l]++
			}
		}
	}
	if nfiles == 0 {
		return nil, nil, 0, fmt.Errorf("%s: no non-test Go files", dir)
	}
	return decls, lines, headers, nil
}

// inRange reports whether a comment group sits inside the import declarations or on the package line.
func inRange(fs *token.FileSet, g *ast.CommentGroup, skip map[int]bool) bool {
	return skip[fs.Position(g.Pos()).Line]
}

func key(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		k := "func "
		if d.Recv != nil && len(d.Recv.List) == 1 {
			var b bytes.Buffer
			_ = printer.Fprint(&b, token.NewFileSet(), d.Recv.List[0].Type)
			k += "(" + b.String() + ") "
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
