package httpx

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// Raw JSON. A json.RawMessage field keeps the request's bytes as they came, so
// they would reach a jsonb column without the decoder's repairs: bytes that are
// not UTF-8 (SQLSTATE 22021), a lone surrogate such as "\ud800" (22P02), a
// number beyond numeric's range (22003). ReadJSON and ReadJSONStripNUL decode
// every RawMessage in the request into an any and marshal it again: bad UTF-8
// and lone surrogates become U+FFFD, numbers become float64 (about 16 digits),
// and a number beyond float64 is a RawJSONError (422). In the request models
// of api and ingest that is FeedbackPost.diagnostics; the sync has none.

// RawJSONError is ReadJSON's error for a json.RawMessage field whose value
// cannot be stored; WriteDecodeError answers it 422.
type RawJSONError struct {
	Field string // its JSON path, e.g. "diagnostics"
	Err   error
}

func (e *RawJSONError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *RawJSONError) Unwrap() error { return e.Err }

var errNumberRange = errors.New("a number is out of range")

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// normalizeRaw re-encodes every json.RawMessage reachable from v (the pointer
// ReadJSON decoded into) through pointers, struct fields, slices, arrays and
// map values. Types that cannot hold one are not walked at all.
func normalizeRaw(v any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || !holdsRaw(rv.Type(), map[reflect.Type]bool{}) {
		return nil
	}
	return walkRaw(rv, "")
}

// holdsRaw says whether a value of type t can contain a json.RawMessage.
func holdsRaw(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == rawMessageType {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return holdsRaw(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if f := t.Field(i); (f.IsExported() || f.Anonymous) && holdsRaw(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

func walkRaw(v reflect.Value, path string) error {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			return walkRaw(v.Elem(), path)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() && !f.Anonymous || !holdsRaw(f.Type, map[reflect.Type]bool{}) {
				continue
			}
			if err := walkRaw(v.Field(i), joinPath(path, jsonName(f))); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if v.Type() == rawMessageType {
			return reencode(v, path)
		}
		fallthrough
	case reflect.Array:
		for i := range v.Len() {
			if err := walkRaw(v.Index(i), path+"[]"); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			if err := walkRaw(e, path+"{}"); err != nil {
				return err
			}
			v.SetMapIndex(k, e)
		}
	}
	return nil
}

// reencode decodes a RawMessage into an any and marshals it again.
func reencode(v reflect.Value, path string) error {
	raw := v.Bytes()
	if len(raw) == 0 {
		return nil
	}
	var x any
	if err := json.Unmarshal(raw, &x); err != nil {
		// The outer decoder checked the syntax: what fails here is a number
		// beyond float64 ("cannot unmarshal number 1e400 into … float64").
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			err = errNumberRange
		}
		return &RawJSONError{Field: path, Err: err}
	}
	out, err := json.Marshal(x)
	if err != nil {
		return &RawJSONError{Field: path, Err: err}
	}
	v.SetBytes(out)
	return nil
}

func jsonName(f reflect.StructField) string {
	if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
		return name
	}
	return f.Name
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
