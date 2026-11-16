package db

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// execParam is what pgx sends for arg with default_query_exec_mode=exec (the services on Cloud
// Run): no parameter OID, the text format, the value typed by its Go type alone. It is pgx's own
// builder (pgx.ExtendedQueryBuilder with no statement description, as Conn does in that mode), so
// no server is needed. ok is false when pgx cannot encode arg at all.
func execParam(t *testing.T, m *pgtype.Map, arg any) (text string, null, ok bool) {
	t.Helper()
	var eqb pgx.ExtendedQueryBuilder
	if err := eqb.Build(m, nil, []any{arg}); err != nil {
		return err.Error(), false, false
	}
	if eqb.ParamFormats[0] != pgtype.TextFormatCode {
		t.Fatalf("%T goes out in format %d, want text", arg, eqb.ParamFormats[0])
	}
	return string(eqb.ParamValues[0]), eqb.ParamValues[0] == nil, true
}

type namedKind string

// The store's parameter types in exec mode, on a type map as AfterConnect leaves it
// (registerTypes): each is the text Postgres reads as the type the query gives it. Two pins are
// warnings: a []byte goes out as bytea (\x…), which a jsonb parameter rejects, so jsonb is passed
// as a string (or json.RawMessage); and a slice of a named type other than uuid.UUID has no type
// at all. CI's pooler job runs the store's own queries this way through PgBouncer.
func TestExecModeEncoding(t *testing.T) {
	m := pgtype.NewMap()
	registerTypes(m)
	a := uuid.MustParse("6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b")
	b := uuid.MustParse("0b6d1c9e-2f3a-4b5c-8d7e-1a2b3c4d5e6f")
	var noIDs []uuid.UUID
	var noFloat *float64
	tenth := 0.1
	f := tenth + 0.2 // at run time: a constant expression would fold to 0.3
	for _, c := range []struct {
		name string
		arg  any
		want string // the text; "NULL" for a NULL
	}{
		{"uuid", a, a.String()},
		{"*uuid", &a, a.String()},
		{"[]uuid (registerTypes)", []uuid.UUID{a, b}, "{" + a.String() + "," + b.String() + "}"},
		{"empty []uuid", []uuid.UUID{}, "{}"},
		{"nil []uuid", noIDs, "NULL"},
		{"text", `it's "q" \ ü`, `it's "q" \ ü`},
		{"int", 42, "42"},
		{"int64", int64(-7), "-7"},
		{"float64, shortest exact", f, "0.30000000000000004"},
		{"nil *float64", noFloat, "NULL"},
		{"bool", true, "t"},
		{"timestamptz, in UTC with microseconds", time.Date(2026, 11, 15, 6, 30, 15, 123456000, time.FixedZone("CET", 3600)),
			"2026-11-15 05:30:15.123456Z"},
		{"date as the store passes it", "2026-11-15", "2026-11-15"},
		// pgx 5.12 quotes every element that holds whitespace anywhere (5.9 only at either end) or the
		// type's delimiter. Postgres reads "b c" and b c as the same element, and store's TestPooler reads
		// these back through the pooler (D232).
		{"[]string, quoted where needed", []string{"a", "b c", "", "NULL", " x", "{,}"}, `{a,"b c","","NULL"," x","{,}"}`},
		{"[]string, escapes", []string{"tab\there", `q"uote`, `back\slash`}, `{"tab` + "\t" + `here","q\"uote","back\\slash"}`},
		{"[]bool", []bool{true, false}, "{t,f}"},
		{"[]int64", []int64{1, 2}, "{1,2}"},
		{"jsonb as a string", `{"a":1}`, `{"a":1}`},
		{"jsonb as json.RawMessage", json.RawMessage(`{"a":1}`), `{"a":1}`},
		{"a []byte is bytea, never jsonb", []byte(`{}`), `\x7b7d`},
		{"a named string", namedKind("hr"), "hr"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, null, ok := execParam(t, m, c.arg)
			if !ok {
				t.Fatalf("pgx cannot encode %T in exec mode: %s", c.arg, got)
			}
			if null {
				got = "NULL"
			}
			if got != c.want {
				t.Fatalf("%T goes out as %q, want %q", c.arg, got, c.want)
			}
		})
	}

	if got, _, ok := execParam(t, m, []namedKind{"hr"}); ok || !strings.Contains(got, "cannot find encode plan") {
		t.Errorf("a slice of a named string encoded (%q): if pgx learned it, this pin can go", got)
	}
	if got, _, ok := execParam(t, pgtype.NewMap(), []uuid.UUID{a}); ok {
		t.Errorf("[]uuid.UUID encodes without registerTypes (%q): pgx learned it, the registration can go", got)
	}
}

// What the pool does on a connection of its own, besides the queries it is given: no startup
// parameter, no hook that sends a statement (BeforeConnect, AfterRelease, BeforeClose, pgconn's
// AfterConnect and ValidateConnect stay unset), and AfterConnect, which only calls registerTypes.
// In transaction mode a session-level statement would land on a server connection the pooler hands
// to the next client (design §4).
func TestPoolConfigHooks(t *testing.T) {
	cloudRunEnv(t, "")
	cfg, err := PoolConfig("postgres://u@127.0.0.1:1/db?sslmode=disable", 3)
	if err != nil {
		t.Fatal(err)
	}
	cc := cfg.ConnConfig
	switch {
	case cfg.AfterConnect == nil:
		t.Error("AfterConnect is unset: the timestamptz and uuid[] mappings are gone")
	case cfg.BeforeConnect != nil, cfg.AfterRelease != nil, cfg.BeforeClose != nil:
		t.Error("a pool hook is set: check that it sends no session-level statement, then update this test")
	case cc.AfterConnect != nil, cc.ValidateConnect != nil:
		t.Error("a pgconn hook runs on every new server connection: check it, then update this test")
	case len(cc.RuntimeParams) != 0:
		t.Errorf("startup parameters %v: a transaction-mode pooler refuses unknown ones", cc.RuntimeParams)
	case cfg.MaxConns != 3 || cfg.MaxConnLifetime != 30*time.Minute:
		t.Errorf("MaxConns %d, MaxConnLifetime %v", cfg.MaxConns, cfg.MaxConnLifetime)
	}
}
