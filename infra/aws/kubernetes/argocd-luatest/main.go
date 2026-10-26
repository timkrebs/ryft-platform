// argocd-luatest evaluates the custom health checks of infra/aws/kubernetes/argocd.tf
// (every resource.customizations.health.* key of the Argo CD ConfigMap) in a Lua VM
// that has exactly the libraries of Argo CD v3.5.6, against table-driven objects.
//
// Why: Argo CD runs these scripts without the string library, so a call such as
// string.find(c.reason, "Fail") fails in production ("attempt to index a non-table
// object(nil) with key 'find'") and the app goes to ComparisonError, while a lint or a
// terraform test cannot see it (2026-10-26, found by the kind e2e).
//
// Usage: go run . [path/to/argocd.tf]   (default ../argocd.tf)
//
// The scripts are read from the HCL itself (no providers, no state, no credentials):
// every object key beginning with the prefix below, as a literal or as the key of a
// for expression over a literal list, with its heredoc value.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	lua "github.com/yuin/gopher-lua"
	"github.com/zclconf/go-cty/cty"
)

const prefix = "resource.customizations.health."

func main() {
	path := "../argocd.tf"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	scripts, err := extract(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	failures := 0
	fail := func(format string, a ...any) {
		failures++
		fmt.Printf("FAIL  "+format+"\n", a...)
	}

	// The VM must be the production one: no string library, not even through the string
	// metatable.
	for _, probe := range []struct{ name, src string }{
		{"string.find", `return string.find("a", "a")`},
		{"s:find", `local s = "abc"; return s:find("b")`},
		{"math.floor", `return math.floor(1.5)`},
		// util/lua/oslib_safe.go registers its functions into `table`, so the global os stays
		// nil; only require("os") works (PreloadModule in lua.go).
		{"global os.time", `return os.time()`},
	} {
		if _, err := run(probe.src, nil); err == nil {
			fail("library set: %s works, the VM has more libraries than Argo CD's", probe.name)
		}
	}
	if _, err := run(`local t = require("os").time(); hs = { status = "Healthy", message = "" }; if t == nil then error("no time") end; return hs`, nil); err != nil {
		fail("library set: require(\"os\").time() must work as in Argo CD: %v", err)
	}

	// Every script that exists must be covered, and every case must have a script.
	cases := allCases()
	keys := make([]string, 0, len(scripts))
	for k := range scripts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(cases[k]) == 0 {
			fail("%s: no test cases", k)
		}
	}
	for k := range cases {
		if _, ok := scripts[k]; !ok {
			fail("%s: cases for a script that argocd.tf does not define", k)
		}
	}

	total := 0
	for _, k := range keys {
		src := scripts[k]
		if strings.Contains(src, "string.") || strings.Contains(src, ":find(") {
			fail("%s: uses the string library", k)
		}
		for _, c := range cases[k] {
			total++
			got, err := run(src, c.obj)
			switch {
			case err != nil:
				fail("%s / %s: script error: %v", short(k), c.name, strings.SplitN(err.Error(), "\n", 2)[0])
			case got.status != c.status:
				fail("%s / %s: status %q, want %q (message %q)", short(k), c.name, got.status, c.status, got.message)
			case !strings.Contains(got.message, c.message):
				fail("%s / %s: message %q does not contain %q", short(k), c.name, got.message, c.message)
			case c.status == "Healthy" && got.message != "" && c.message == "":
				fail("%s / %s: Healthy with message %q", short(k), c.name, got.message)
			}
		}
	}
	fmt.Printf("%d scripts, %d cases, %d failures\n", len(keys), total, failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func short(k string) string { return strings.TrimPrefix(k, prefix) }

type result struct{ status, message string }

// run executes a health script as Argo CD does (util/lua/lua.go, runLua and
// ExecuteHealthLua at v3.5.6): SkipOpenLibs, then package, base, table and OpenSafeOs (which adds nothing global),
// plus the preloaded module for require("os");
// the object is the global obj; the script must return a table with a valid status; one
// second budget.
func run(script string, obj map[string]any) (result, error) {
	l := lua.NewState(lua.Options{SkipOpenLibs: true})
	defer l.Close()
	for _, lib := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage},
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.OsLibName, OpenSafeOs},
	} {
		if err := l.CallByParam(lua.P{Fn: l.NewFunction(lib.fn), NRet: 0, Protect: true}, lua.LString(lib.name)); err != nil {
			return result{}, err
		}
	}
	l.PreloadModule(lua.OsLibName, SafeOsLoader)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	l.SetContext(ctx)
	l.SetGlobal("obj", decode(l, obj))
	if err := l.DoString(script); err != nil {
		return result{}, err
	}
	ret := l.Get(-1)
	tbl, ok := ret.(*lua.LTable)
	if !ok {
		return result{}, fmt.Errorf("returned %s, not a table", ret.Type())
	}
	r := result{status: tbl.RawGetString("status").String(), message: ""}
	if m := tbl.RawGetString("message"); m != lua.LNil {
		r.message = m.String()
	}
	switch r.status {
	case "Healthy", "Progressing", "Degraded", "Suspended", "Missing", "Unknown":
	default:
		return result{}, fmt.Errorf("invalid health status %q", r.status)
	}
	return r, nil
}

// decode mirrors decodeValue in Argo CD's util/lua/lua.go.
func decode(l *lua.LState, v any) lua.LValue {
	switch c := v.(type) {
	case bool:
		return lua.LBool(c)
	case float64:
		return lua.LNumber(c)
	case json.Number:
		return lua.LString(c)
	case int:
		return lua.LNumber(c)
	case int32:
		return lua.LNumber(c)
	case int64:
		return lua.LNumber(c)
	case string:
		return lua.LString(c)
	case []any:
		t := l.CreateTable(len(c), 0)
		for _, i := range c {
			t.Append(decode(l, i))
		}
		return t
	case map[string]any:
		t := l.CreateTable(0, len(c))
		for k, i := range c {
			t.RawSetH(lua.LString(k), decode(l, i))
		}
		return t
	case nil:
		return lua.LNil
	}
	// Argo CD maps any other type to nil; a case built with one would test nothing.
	panic(fmt.Sprintf("decode: unsupported type %T in a test object", v))
}

// extract returns key -> Lua source for every health customization in the file.
func extract(path string) (map[string]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	out := map[string]string{}
	add := func(k, v string) error {
		if _, dup := out[k]; dup {
			return fmt.Errorf("%s defined twice", k)
		}
		out[k] = v
		return nil
	}
	var walkErr error
	// A key the extractor cannot evaluate statically must not hide a health script.
	const marker = "customizations.health"
	unevaluable := func(r hcl.Range) {
		if r.Start.Byte >= 0 && r.End.Byte <= len(src) && strings.Contains(string(src[r.Start.Byte:r.End.Byte]), marker) {
			walkErr = fmt.Errorf("%s: a %s key the harness cannot evaluate statically (%s)", path, marker, r)
		}
	}
	hclsyntax.VisitAll(f.Body.(*hclsyntax.Body), func(n hclsyntax.Node) hcl.Diagnostics {
		switch e := n.(type) {
		case *hclsyntax.ObjectConsExpr:
			for _, item := range e.Items {
				k, kd := item.KeyExpr.Value(nil)
				if kd.HasErrors() || k.Type() != cty.String || !strings.HasPrefix(k.AsString(), prefix) {
					unevaluable(item.KeyExpr.Range())
					continue
				}
				v, vd := item.ValueExpr.Value(nil)
				if vd.HasErrors() || v.Type() != cty.String {
					walkErr = fmt.Errorf("%s: the script is not a literal string", k.AsString())
					continue
				}
				if err := add(k.AsString(), v.AsString()); err != nil {
					walkErr = err
				}
			}
		case *hclsyntax.ForExpr:
			coll, cd := e.CollExpr.Value(nil)
			if cd.HasErrors() || e.KeyExpr == nil || !(coll.Type().IsTupleType() || coll.Type().IsListType()) {
				if e.KeyExpr != nil {
					unevaluable(e.KeyExpr.Range())
				}
				return nil
			}
			for it := coll.ElementIterator(); it.Next(); {
				_, el := it.Element()
				ctx := &hcl.EvalContext{Variables: map[string]cty.Value{e.ValVar: el}}
				k, kd := e.KeyExpr.Value(ctx)
				if kd.HasErrors() || k.Type() != cty.String || !strings.HasPrefix(k.AsString(), prefix) {
					unevaluable(e.KeyExpr.Range())
					continue
				}
				v, vd := e.ValExpr.Value(ctx)
				if vd.HasErrors() || v.Type() != cty.String {
					walkErr = fmt.Errorf("%s: the script is not a literal string", k.AsString())
					continue
				}
				if err := add(k.AsString(), v.AsString()); err != nil {
					walkErr = err
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no %s* key found; did the structure of argocd.tf change?", path, prefix)
	}
	return out, nil
}
