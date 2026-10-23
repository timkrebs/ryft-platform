package logx

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// JSONError is a json.Unmarshal error fit for a log line: a type mismatch
// names the number it could not store ("cannot unmarshal number 61.7 into …"),
// and the bodies RYFT decodes carry health values (a scored event's recovery,
// a day's zone minutes). The field and the type are enough to find the bug;
// any other error is returned as it is.
func JSONError(err error) error {
	var te *json.UnmarshalTypeError
	if !errors.As(err, &te) {
		return err
	}
	kind, _, _ := strings.Cut(te.Value, " ") // "number 61.7" → "number"
	into := "Go value"
	switch {
	case te.Struct != "" && te.Field != "":
		into = te.Struct + "." + te.Field
	case te.Field != "":
		into = te.Field
	}
	return fmt.Errorf("json: cannot unmarshal %s into %s of type %v", kind, into, te.Type)
}
