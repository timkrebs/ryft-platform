package model

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// update rewrites testdata/schemas from the Go types:
//
//	go test ./internal/model -run TestSchemaSnapshots -update
//
// Review the resulting diff like any other contract change: docs/API.md is
// additive-only, so a removed or retyped property is a breaking change.
var update = flag.Bool("update", false, "rewrite testdata/schemas from the Go types")

const schemaDir = "testdata/schemas"

var (
	timeType      = reflect.TypeOf(time.Time{})
	rawType       = reflect.TypeOf(json.RawMessage(nil))
	optQuietType  = reflect.TypeOf(OptionalQuiet{})
	unmarshalerIf = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
)

// schemaOf describes t the way encoding/json sees it. Structs of this package
// are referenced by name ($ref), everything else is inlined. A pointer, a map
// or a slice is "nullable" because the wire value may be null.
func schemaOf(t reflect.Type) map[string]any {
	switch {
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case t == rawType:
		return map[string]any{"description": "raw JSON"}
	case t == optQuietType:
		// absent / null / object, see OptionalQuiet.UnmarshalJSON
		return map[string]any{"$ref": "QuietHours", "nullable": true, "description": "absent keeps, null clears"}
	}
	switch t.Kind() {
	case reflect.Ptr:
		s := schemaOf(t.Elem())
		out := map[string]any{"nullable": true}
		for k, v := range s {
			out[k] = v
		}
		return out
	case reflect.Struct:
		if t.PkgPath() == reflect.TypeOf(Sample{}).PkgPath() {
			return map[string]any{"$ref": t.Name()}
		}
		if t.NumField() == 0 || t.Implements(unmarshalerIf) {
			return map[string]any{"description": "custom JSON: " + t.String()}
		}
		// foreign struct (e.g. uuid arrays are handled below); describe by name
		return map[string]any{"description": "foreign type " + t.String()}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "base64", "nullable": true}
		}
		return map[string]any{"type": "array", "nullable": true, "items": schemaOf(t.Elem())}
	case reflect.Array:
		if t.String() == "uuid.UUID" {
			return map[string]any{"type": "string", "format": "uuid"}
		}
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "nullable": true, "additionalProperties": schemaOf(t.Elem())}
	case reflect.Interface:
		return map[string]any{"description": "any JSON value"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	}
	return map[string]any{"description": "unsupported kind " + t.Kind().String()}
}

// structSchema is the snapshot of one wire type.
func structSchema(name string, t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			parts := strings.Split(tag, ",")
			key := parts[0]
			if f.Anonymous && key == "" && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			if !f.IsExported() {
				continue
			}
			if key == "" {
				key = f.Name
			}
			props[key] = schemaOf(f.Type)
			omit := false
			for _, o := range parts[1:] {
				if o == "omitempty" || o == "omitzero" {
					omit = true
				}
			}
			if !omit {
				required = append(required, key)
			}
		}
	}
	walk(t)
	sort.Strings(required)
	return map[string]any{
		"title":                name,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
		"required":             required,
	}
}

func renderSchema(name string, t reflect.Type) string {
	b, err := json.MarshalIndent(structSchema(name, t), "", "  ") // map keys are sorted
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// lineDiff is a small readable diff: the first differing lines, - for the
// snapshot, + for the Go type.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var sb strings.Builder
	shown := 0
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a == b {
			continue
		}
		if shown++; shown > 12 {
			sb.WriteString("  ...\n")
			break
		}
		fmt.Fprintf(&sb, "  line %d\n    - %s\n    + %s\n", i+1, a, b)
	}
	return sb.String()
}

func TestSchemaSnapshots(t *testing.T) {
	names := make([]string, 0, len(contractTypes))
	for n := range contractTypes {
		names = append(names, n)
	}
	sort.Strings(names)

	if *update {
		if err := os.MkdirAll(schemaDir, 0o755); err != nil {
			t.Fatal(err)
		}
		old, _ := filepath.Glob(filepath.Join(schemaDir, "*.json"))
		for _, f := range old {
			if _, ok := contractTypes[strings.TrimSuffix(filepath.Base(f), ".json")]; !ok {
				_ = os.Remove(f)
			}
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(schemaDir, n+".json"), []byte(renderSchema(n, contractTypes[n])), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}

	for _, n := range names {
		n := n
		t.Run(n, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(schemaDir, n+".json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no snapshot for model.%s (%v); a new wire type needs one: go test ./internal/model -run TestSchemaSnapshots -update", n, err)
			}
			got := renderSchema(n, contractTypes[n])
			if string(want) != got {
				t.Errorf("model.%s no longer matches %s.\n%s"+
					"If the change is intended it must be additive and documented in docs/API.md "+
					"(changelog entry, Swift mirror); then regenerate: go test ./internal/model -run TestSchemaSnapshots -update",
					n, path, lineDiff(string(want), got))
			}
		})
	}

	// A snapshot without a type means a type was removed or renamed.
	files, _ := filepath.Glob(filepath.Join(schemaDir, "*.json"))
	for _, f := range files {
		n := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, ok := contractTypes[n]; !ok {
			t.Errorf("%s has no model.%s any more (removed or renamed: a breaking change for v1); update the snapshot only with the api-contract-owner's OK", f, n)
		}
	}
}

// TestContractTypesComplete keeps contractTypes honest: every exported struct
// declared in this package must be in it.
func TestContractTypesComplete(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, s := range gd.Specs {
					ts := s.(*ast.TypeSpec)
					if _, isStruct := ts.Type.(*ast.StructType); isStruct && ts.Name.IsExported() {
						declared[ts.Name.Name] = true
					}
				}
			}
		}
	}
	for n := range declared {
		if _, ok := contractTypes[n]; !ok {
			t.Errorf("struct model.%s is not in contractTypes (contract_types_test.go): add it, then run -update", n)
		}
	}
	for n := range contractTypes {
		if !declared[n] {
			t.Errorf("contractTypes lists model.%s, which is not declared in the package", n)
		}
	}
}
